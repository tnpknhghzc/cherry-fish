// Package store сохраняет проиндексированные книги, векторы и чаты на диск.
// Всё лежит в одной папке данных; книга идентифицируется хэшем содержимого файла.
package store

import (
	"compress/gzip"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"

	"github.com/tnpknhghzc/locallm/internal/chunk"
	"github.com/tnpknhghzc/locallm/internal/vec"
)

type Meta struct {
	ID         string    `json:"id"`
	Title      string    `json:"title"`
	Author     string    `json:"author"`
	Format     string    `json:"format"`
	SourcePath string    `json:"source_path"`
	Pages      int       `json:"pages"`
	Sections   int       `json:"sections"`
	Chunks     int       `json:"chunks"`
	EmbedderID string    `json:"embedder_id"`
	CreatedAt  time.Time `json:"created_at"`
}

type Source struct {
	Chunk    int    `json:"chunk"`
	Section  string `json:"section"`
	PageFrom int    `json:"page_from"`
	PageTo   int    `json:"page_to"`
	Snippet  string `json:"snippet"`
}

type Message struct {
	Role    string    `json:"role"` // user | assistant
	Content string    `json:"content"`
	Sources []Source  `json:"sources,omitempty"`
	Time    time.Time `json:"time"`
}

type Chat struct {
	ID       string    `json:"id"`
	Messages []Message `json:"messages"`
}

type Store struct {
	root string
	mu   sync.Mutex
}

func Open(root string) (*Store, error) {
	if err := os.MkdirAll(filepath.Join(root, "books"), 0o755); err != nil {
		return nil, err
	}
	return &Store{root: root}, nil
}

func (s *Store) bookDir(id string) string { return filepath.Join(s.root, "books", id) }

// FileID — идентификатор книги: первые 16 hex-символов SHA-256 файла.
func FileID(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil))[:16], nil
}

var idRe = regexp.MustCompile(`^[0-9a-f]{16}$`)
var chatRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func ValidID(id string) bool     { return idRe.MatchString(id) }
func ValidChatID(id string) bool { return chatRe.MatchString(id) }

func writeJSON(path string, v any, gz bool) error {
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	var w io.Writer = f
	var zw *gzip.Writer
	if gz {
		zw = gzip.NewWriter(f)
		w = zw
	}
	err = json.NewEncoder(w).Encode(v)
	if zw != nil {
		if e := zw.Close(); err == nil {
			err = e
		}
	}
	if e := f.Close(); err == nil {
		err = e
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

func readJSON(path string, v any, gz bool) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	var r io.Reader = f
	if gz {
		zr, err := gzip.NewReader(f)
		if err != nil {
			return err
		}
		defer zr.Close()
		r = zr
	}
	return json.NewDecoder(r).Decode(v)
}

func (s *Store) Exists(id string) bool {
	_, err := os.Stat(filepath.Join(s.bookDir(id), "meta.json"))
	return err == nil
}

func (s *Store) SaveBook(m Meta, t *chunk.Tree) error {
	dir := s.bookDir(m.ID)
	if err := os.MkdirAll(filepath.Join(dir, "chats"), 0o755); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(dir, "tree.json.gz"), t, true); err != nil {
		return err
	}
	return writeJSON(filepath.Join(dir, "meta.json"), m, false) // meta пишется последней: её наличие = книга цела
}

func (s *Store) LoadBook(id string) (Meta, *chunk.Tree, error) {
	var m Meta
	var t chunk.Tree
	if !ValidID(id) {
		return m, nil, errors.New("неверный id книги")
	}
	if err := readJSON(filepath.Join(s.bookDir(id), "meta.json"), &m, false); err != nil {
		return m, nil, err
	}
	if err := readJSON(filepath.Join(s.bookDir(id), "tree.json.gz"), &t, true); err != nil {
		return m, nil, err
	}
	return m, &t, nil
}

func (s *Store) List() []Meta {
	ents, _ := os.ReadDir(filepath.Join(s.root, "books"))
	var out []Meta
	for _, e := range ents {
		var m Meta
		if readJSON(filepath.Join(s.root, "books", e.Name(), "meta.json"), &m, false) == nil {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

func (s *Store) Delete(id string) error {
	if !ValidID(id) {
		return errors.New("неверный id книги")
	}
	return os.RemoveAll(s.bookDir(id))
}

// ---------- векторы: append-only, устойчиво к прерыванию ----------

const vecMagic = "LLVA"

func (s *Store) vecPath(id string) string { return filepath.Join(s.bookDir(id), "vectors.bin") }

// AppendVectors дописывает строки; файл создаётся с заголовком (dims).
func (s *Store) AppendVectors(id string, dims int, vs [][]float32) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.vecPath(id)
	f, err := os.OpenFile(p, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	st, _ := f.Stat()
	if st.Size() == 0 {
		hdr := make([]byte, 8)
		copy(hdr, vecMagic)
		binary.LittleEndian.PutUint32(hdr[4:], uint32(dims))
		if _, err := f.Write(hdr); err != nil {
			return err
		}
		st, _ = f.Stat()
	} else {
		// отрезаем возможный «хвост» недописанной строки после сбоя
		rows := (st.Size() - 8) / int64(4*dims)
		if err := f.Truncate(8 + rows*int64(4*dims)); err != nil {
			return err
		}
	}
	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		return err
	}
	buf := make([]byte, 0, len(vs)*dims*4)
	for _, v := range vs {
		for _, x := range v {
			buf = binary.LittleEndian.AppendUint32(buf, math.Float32bits(x))
		}
	}
	_, err = f.Write(buf)
	return err
}

// LoadVectors читает сохранённые векторы. Нет файла → пустой индекс (dims=0).
func (s *Store) LoadVectors(id string, wantDims int) (*vec.Index, error) {
	b, err := os.ReadFile(s.vecPath(id))
	if errors.Is(err, os.ErrNotExist) {
		return vec.New(wantDims), nil
	}
	if err != nil {
		return nil, err
	}
	if len(b) < 8 || string(b[:4]) != vecMagic {
		return vec.New(wantDims), nil
	}
	dims := int(binary.LittleEndian.Uint32(b[4:]))
	if dims != wantDims {
		return vec.New(wantDims), nil // другая модель эмбеддингов → переиндексация
	}
	rows := (len(b) - 8) / (4 * dims)
	x := vec.New(dims)
	for r := 0; r < rows; r++ {
		v := make([]float32, dims)
		for i := range v {
			v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[8+4*(r*dims+i):]))
		}
		if err := x.Add(v); err != nil {
			return nil, err
		}
	}
	return x, nil
}

func (s *Store) ResetVectors(id string) error {
	err := os.Remove(s.vecPath(id))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// ---------- чаты и сводки ----------

func (s *Store) chatPath(book, chat string) string {
	return filepath.Join(s.bookDir(book), "chats", chat+".json")
}

func (s *Store) LoadChat(book, chat string) (*Chat, error) {
	if !ValidID(book) || !ValidChatID(chat) {
		return nil, errors.New("неверный идентификатор")
	}
	c := &Chat{ID: chat}
	err := readJSON(s.chatPath(book, chat), c, false)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	return c, err
}

func (s *Store) SaveChat(book string, c *Chat) error {
	if !ValidID(book) || !ValidChatID(c.ID) {
		return errors.New("неверный идентификатор")
	}
	if err := os.MkdirAll(filepath.Join(s.bookDir(book), "chats"), 0o755); err != nil {
		return err
	}
	return writeJSON(s.chatPath(book, c.ID), c, false)
}

func (s *Store) ListChats(book string) []string {
	ents, _ := os.ReadDir(filepath.Join(s.bookDir(book), "chats"))
	var out []string
	for _, e := range ents {
		if n := e.Name(); filepath.Ext(n) == ".json" {
			out = append(out, n[:len(n)-5])
		}
	}
	return out
}

func (s *Store) DeleteChat(book, chat string) error {
	if !ValidID(book) || !ValidChatID(chat) {
		return errors.New("неверный идентификатор")
	}
	return os.Remove(s.chatPath(book, chat))
}

// Summaries: ключ — id раздела (строкой) или "book".
func (s *Store) LoadSummaries(book string) map[string]string {
	m := map[string]string{}
	_ = readJSON(filepath.Join(s.bookDir(book), "summaries.json"), &m, false)
	return m
}

func (s *Store) SaveSummaries(book string, m map[string]string) error {
	return writeJSON(filepath.Join(s.bookDir(book), "summaries.json"), m, false)
}

// SaveMeta обновляет только метаданные книги.
func (s *Store) SaveMeta(m Meta) error {
	return writeJSON(filepath.Join(s.bookDir(m.ID), "meta.json"), m, false)
}
