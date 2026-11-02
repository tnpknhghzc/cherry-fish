// Package server — локальный HTTP-API поверх движка для фронтендов не на Go.
// Слушает только loopback, требует токен, проверяет Host (защита от DNS rebinding).
// Генерация ответов по-прежнему идёт внутри процесса — к этому серверу ходит только UI.
package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/tnpknhghzc/locallm/internal/hw"
	"github.com/tnpknhghzc/locallm/pkg/locallm"
)

//go:embed ui/index.html
var uiHTML string

type Server struct {
	Eng          *locallm.Engine
	Token        string
	AllowOrigins []string
	Version      string

	mu    sync.Mutex
	books map[string]*locallm.Book
	jobs  map[string]*job
}

type job struct {
	Stage  string `json:"stage"`
	Done   int    `json:"done"`
	Total  int    `json:"total"`
	Error  string `json:"error,omitempty"`
	cancel context.CancelFunc
}

// LoadOrCreateToken читает токен из файла данных или создаёт новый (0600).
func LoadOrCreateToken(dataDir string) (string, error) {
	p := filepath.Join(dataDir, "server.token")
	if b, err := os.ReadFile(p); err == nil && len(b) >= 32 {
		return strings.TrimSpace(string(b)), nil
	}
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	tok := hex.EncodeToString(raw)
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return "", err
	}
	return tok, os.WriteFile(p, []byte(tok), 0o600)
}

func New(e *locallm.Engine, token string) *Server {
	return &Server{Eng: e, Token: token, books: map[string]*locallm.Book{}, jobs: map[string]*job{}}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", func(w http.ResponseWriter, r *http.Request) {
		js(w, 200, map[string]any{"ok": true, "version": s.Version})
	})
	mux.HandleFunc("GET /{$}", s.ui)
	mux.HandleFunc("GET /v1/info", s.info)
	mux.HandleFunc("GET /v1/workspace", s.getWorkspace)
	mux.HandleFunc("PUT /v1/workspace", s.putWorkspace)
	mux.HandleFunc("POST /v1/upload", s.upload)
	mux.HandleFunc("GET /v1/books", s.listBooks)
	mux.HandleFunc("POST /v1/books", s.addBook)
	mux.HandleFunc("GET /v1/books/{id}", s.getBook)
	mux.HandleFunc("DELETE /v1/books/{id}", s.deleteBook)
	mux.HandleFunc("GET /v1/books/{id}/toc", s.toc)
	mux.HandleFunc("POST /v1/books/{id}/ask", s.ask)
	mux.HandleFunc("POST /v1/books/{id}/search", s.search)
	mux.HandleFunc("POST /v1/books/{id}/summary", s.summary)
	mux.HandleFunc("GET /v1/books/{id}/chats", s.chats)
	mux.HandleFunc("GET /v1/books/{id}/chats/{chat}", s.getChat)
	mux.HandleFunc("DELETE /v1/books/{id}/chats/{chat}", s.deleteChat)
	return s.guard(mux)
}

func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			host = r.Host
		}
		if host != "127.0.0.1" && host != "localhost" && host != "[::1]" && host != "::1" {
			http.Error(w, "forbidden host", 403)
			return
		}
		if o := r.Header.Get("Origin"); o != "" {
			ok := false
			for _, a := range s.AllowOrigins {
				if a == "*" || a == o {
					ok = true
				}
			}
			if !ok {
				http.Error(w, "forbidden origin", 403)
				return
			}
			w.Header().Set("Access-Control-Allow-Origin", o)
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
			w.Header().Set("Vary", "Origin")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(204)
			return
		}
		if r.URL.Path != "/v1/health" && r.URL.Path != "/" {
			got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if subtle.ConstantTimeCompare([]byte(got), []byte(s.Token)) != 1 {
				http.Error(w, "unauthorized", 401)
				return
			}
		}
		if r.URL.Path != "/v1/upload" {
			r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		}
		next.ServeHTTP(w, r)
	})
}

func js(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, err error) {
	js(w, code, map[string]string{"error": err.Error()})
}

func (s *Server) book(id string) (*locallm.Book, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if b, ok := s.books[id]; ok {
		return b, nil
	}
	b, err := s.Eng.OpenBook(id)
	if err != nil {
		return nil, err
	}
	s.books[id] = b
	s.resumeIndexLocked(b)
	return b, nil
}

// resumeIndexLocked запускает достройку векторного индекса, если она не закончена
// (например, после перезапуска программы). Вызывать под s.mu.
func (s *Server) resumeIndexLocked(b *locallm.Book) {
	d, t := b.IndexState()
	if d >= t || s.jobs[b.ID()] != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	j := &job{Stage: "embed", Total: t, Done: d, cancel: cancel}
	s.jobs[b.ID()] = j
	go func() {
		err := b.Index(ctx, func(p locallm.Progress) {
			s.mu.Lock()
			j.Stage, j.Done, j.Total = p.Stage, p.Done, p.Total
			s.mu.Unlock()
		})
		s.mu.Lock()
		if err != nil && ctx.Err() == nil {
			j.Error = err.Error()
			s.mu.Unlock()
			return // оставляем job с ошибкой, чтобы UI её показал
		}
		delete(s.jobs, b.ID())
		s.mu.Unlock()
	}()
}

func (s *Server) bookInfo(b *locallm.Book) map[string]any {
	done, total := b.IndexState()
	m := map[string]any{"meta": b.Meta(), "vectors_done": done, "vectors_total": total, "ready": done == total}
	s.mu.Lock()
	if j := s.jobs[b.ID()]; j != nil {
		m["job"] = j
	}
	s.mu.Unlock()
	return m
}

func (s *Server) listBooks(w http.ResponseWriter, r *http.Request) {
	js(w, 200, s.Eng.Books())
}

func (s *Server) addBook(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Path == "" {
		fail(w, 400, fmt.Errorf("нужно поле path"))
		return
	}
	s.addPath(w, r, req.Path)
}

func (s *Server) addPath(w http.ResponseWriter, r *http.Request, path string) {
	b, err := s.Eng.AddBook(r.Context(), path, nil)
	if err != nil {
		fail(w, 422, err)
		return
	}
	s.mu.Lock()
	s.books[b.ID()] = b
	s.resumeIndexLocked(b)
	s.mu.Unlock()
	js(w, 202, s.bookInfo(b))
}

// upload принимает файл из браузера (multipart, поле "file"), индексирует и удаляет временную копию.
func (s *Server) upload(w http.ResponseWriter, r *http.Request) {
	mr, err := r.MultipartReader()
	if err != nil {
		fail(w, 400, err)
		return
	}
	dir, err := os.MkdirTemp(filepath.Join(s.Eng.DataDir()), "upload-")
	if err != nil {
		fail(w, 500, err)
		return
	}
	defer os.RemoveAll(dir)
	var saved string
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			fail(w, 400, err)
			return
		}
		if part.FormName() != "file" {
			continue
		}
		name := filepath.Base(part.FileName())
		if _, _, err := mime.ParseMediaType("attachment; filename=" + fmt.Sprintf("%q", name)); err != nil || name == "." || name == "" {
			name = "book"
		}
		dst := filepath.Join(dir, name)
		f, err := os.Create(dst)
		if err != nil {
			fail(w, 500, err)
			return
		}
		_, err = io.Copy(f, part)
		f.Close()
		if err != nil {
			fail(w, 500, err)
			return
		}
		saved = dst
		break
	}
	if saved == "" {
		fail(w, 400, fmt.Errorf("в запросе нет файла (поле file)"))
		return
	}
	s.addPath(w, r, saved)
}

func (s *Server) ui(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	// UI не может ходить никуда, кроме этого же сервера
	w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'; connect-src 'self'; img-src 'self' data:; base-uri 'none'; form-action 'none'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	io.WriteString(w, strings.Replace(uiHTML, "__LOCALLM_TOKEN__", s.Token, 1))
}

func (s *Server) info(w http.ResponseWriter, r *http.Request) {
	l, e := s.Eng.ModelInfo()
	m := hw.Memory()
	js(w, 200, map[string]any{
		"version": s.Version, "llm": filepath.Base(l), "embed": filepath.Base(e), "data_dir": s.Eng.DataDir(),
		"ram_gb": float64(m.TotalRAM) / (1 << 30), "threads": hw.Threads(), "os": runtime.GOOS,
	})
}

func (s *Server) wsPath() string { return filepath.Join(s.Eng.DataDir(), "workspace.json") }

func (s *Server) getWorkspace(w http.ResponseWriter, r *http.Request) {
	b, err := os.ReadFile(s.wsPath())
	if err != nil {
		b = []byte("{}")
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Write(b)
}

func (s *Server) putWorkspace(w http.ResponseWriter, r *http.Request) {
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16<<20))
	if err != nil || !json.Valid(b) {
		fail(w, 400, fmt.Errorf("неверный JSON"))
		return
	}
	tmp := s.wsPath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		fail(w, 500, err)
		return
	}
	if err := os.Rename(tmp, s.wsPath()); err != nil {
		fail(w, 500, err)
		return
	}
	w.WriteHeader(204)
}

func (s *Server) getBook(w http.ResponseWriter, r *http.Request) {
	b, err := s.book(r.PathValue("id"))
	if err != nil {
		fail(w, 404, err)
		return
	}
	js(w, 200, s.bookInfo(b))
}

func (s *Server) deleteBook(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.mu.Lock()
	if j := s.jobs[id]; j != nil {
		j.cancel()
	}
	delete(s.books, id)
	s.mu.Unlock()
	if err := s.Eng.DeleteBook(id); err != nil {
		fail(w, 400, err)
		return
	}
	w.WriteHeader(204)
}

func (s *Server) toc(w http.ResponseWriter, r *http.Request) {
	b, err := s.book(r.PathValue("id"))
	if err != nil {
		fail(w, 404, err)
		return
	}
	js(w, 200, b.TOC())
}

func (s *Server) ask(w http.ResponseWriter, r *http.Request) {
	b, err := s.book(r.PathValue("id"))
	if err != nil {
		fail(w, 404, err)
		return
	}
	var req struct {
		Question string `json:"question"`
		ChatID   string `json:"chat_id"`
		Stream   bool   `json:"stream"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, 400, err)
		return
	}
	if !req.Stream {
		ans, err := b.Ask(r.Context(), locallm.AskRequest{ChatID: req.ChatID, Question: req.Question}, nil)
		if err != nil {
			fail(w, 500, err)
			return
		}
		js(w, 200, ans)
		return
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		fail(w, 500, fmt.Errorf("streaming не поддерживается"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	ev := func(name string, v any) {
		bs, _ := json.Marshal(v)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, bs)
		fl.Flush()
	}
	ans, err := b.Ask(r.Context(), locallm.AskRequest{ChatID: req.ChatID, Question: req.Question},
		func(t string) { ev("token", map[string]string{"t": t}) })
	if err != nil {
		ev("error", map[string]string{"error": err.Error()})
		return
	}
	ev("done", ans)
}

func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	b, err := s.book(r.PathValue("id"))
	if err != nil {
		fail(w, 404, err)
		return
	}
	var req struct {
		Query string `json:"query"`
		K     int    `json:"k"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, 400, err)
		return
	}
	ps, err := b.Search(r.Context(), req.Query, req.K)
	if err != nil {
		fail(w, 500, err)
		return
	}
	js(w, 200, ps)
}

func (s *Server) summary(w http.ResponseWriter, r *http.Request) {
	b, err := s.book(r.PathValue("id"))
	if err != nil {
		fail(w, 404, err)
		return
	}
	var req struct {
		Section *int `json:"section"` // нет поля или -1 → вся книга
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	sec := -1
	if req.Section != nil {
		sec = *req.Section
	}
	txt, err := b.SummarizeSection(r.Context(), sec, nil)
	if err != nil {
		fail(w, 500, err)
		return
	}
	js(w, 200, map[string]string{"summary": txt})
}

func (s *Server) chats(w http.ResponseWriter, r *http.Request) {
	b, err := s.book(r.PathValue("id"))
	if err != nil {
		fail(w, 404, err)
		return
	}
	js(w, 200, b.Chats())
}

func (s *Server) getChat(w http.ResponseWriter, r *http.Request) {
	b, err := s.book(r.PathValue("id"))
	if err != nil {
		fail(w, 404, err)
		return
	}
	c, err := b.Chat(r.PathValue("chat"))
	if err != nil {
		fail(w, 400, err)
		return
	}
	js(w, 200, c)
}

func (s *Server) deleteChat(w http.ResponseWriter, r *http.Request) {
	b, err := s.book(r.PathValue("id"))
	if err != nil {
		fail(w, 404, err)
		return
	}
	if err := b.DeleteChat(r.PathValue("chat")); err != nil {
		fail(w, 400, err)
		return
	}
	w.WriteHeader(204)
}
