// Package locallm — локальный ИИ-модуль для работы с большими книгами и документами.
// Всё выполняется внутри процесса: llama.cpp вкомпилирован в бинарник, сетевых
// запросов нет. Пакет можно использовать напрямую из Go-приложения (например, Wails).
package locallm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/tnpknhghzc/locallm/internal/chunk"
	"github.com/tnpknhghzc/locallm/internal/embed"
	"github.com/tnpknhghzc/locallm/internal/extract"
	"github.com/tnpknhghzc/locallm/internal/hw"
	"github.com/tnpknhghzc/locallm/internal/llm"
	"github.com/tnpknhghzc/locallm/internal/models"
	"github.com/tnpknhghzc/locallm/internal/rag"
	"github.com/tnpknhghzc/locallm/internal/store"
	"github.com/tnpknhghzc/locallm/internal/vec"
)

type (
	Source   = store.Source
	Message  = store.Message
	Chat     = store.Chat
	Section  = chunk.Section
	Passage  = rag.Passage
	BookMeta = store.Meta
)

// Options — все поля необязательны: по умолчанию всё подбирается автоматически.
type Options struct {
	DataDir   string // где хранить индексы и чаты (по умолчанию — папка настроек пользователя)
	ModelsDir string // дополнительная папка с .gguf (по умолчанию ищется рядом с программой)
	Profile   string // "fast" | "balanced" | "quality"

	Threads          int // 0 — авто
	ContextSize      int // 0 — по объёму RAM
	GPULayers        int // 0 — только CPU (на macOS Metal включается автоматически)
	MaxContextTokens int // потолок токенов под найденные фрагменты (меньше — быстрее ответ)
	MaxAnswerTokens  int

	Log func(string) // необязательный лог прогресса

	// Для тестов и расширений:
	LLM      llm.LLM
	Embedder embed.Embedder
}

type Progress struct {
	Stage   string // "extract" | "embed" | "ready"
	Done    int
	Total   int
	Message string
}

type Engine struct {
	opt   Options
	store *store.Store
	emb   embed.Embedder
	set   models.Set

	llmOnce sync.Once
	llm     llm.LLM
	llmErr  error
}

func DefaultDataDir() string {
	if d := os.Getenv("LOCALLM_DATA"); d != "" {
		return d
	}
	if d, err := os.UserConfigDir(); err == nil {
		return filepath.Join(d, "locallm")
	}
	return filepath.Join(os.TempDir(), "locallm")
}

func Open(opt Options) (*Engine, error) {
	if opt.DataDir == "" {
		opt.DataDir = DefaultDataDir()
	}
	if opt.Log == nil {
		opt.Log = func(string) {}
	}
	if opt.Threads == 0 {
		opt.Threads = hw.Threads()
	}
	if v, err := strconv.Atoi(os.Getenv("LOCALLM_GPU_LAYERS")); err == nil && opt.GPULayers == 0 {
		opt.GPULayers = v
	}
	if opt.ContextSize == 0 {
		opt.ContextSize = 4096
		if hw.Memory().TotalRAM >= 15<<30 {
			opt.ContextSize = 6144
		}
	}
	st, err := store.Open(opt.DataDir)
	if err != nil {
		return nil, fmt.Errorf("папка данных: %w", err)
	}
	e := &Engine{opt: opt, store: st, emb: opt.Embedder, llm: opt.LLM}
	if e.emb == nil || e.llm == nil {
		e.set, err = models.Prepare(opt.ModelsDir, opt.DataDir, opt.Profile, opt.Log)
		if err != nil {
			return nil, err
		}
	}
	if e.emb == nil {
		q, d := models.EmbedPrefixes(e.set.Embed.Path)
		e.emb, err = embed.NewLlama(e.set.Embed.Path, opt.Threads, q, d)
		if err != nil {
			return nil, err
		}
	}
	return e, nil
}

// ModelInfo — какие модели выбраны (для диагностики).
func (e *Engine) ModelInfo() (llmPath, embedPath string) { return e.set.LLM.Path, e.set.Embed.Path }

// DataDir — папка данных (индексы, чаты, доски).
func (e *Engine) DataDir() string { return e.opt.DataDir }

func (e *Engine) Close() {
	if e.emb != nil {
		e.emb.Close()
	}
	if e.llm != nil {
		e.llm.Close()
	}
}

// getLLM загружает языковую модель при первом обращении (индексация её не требует).
func (e *Engine) getLLM() (llm.LLM, error) {
	e.llmOnce.Do(func() {
		if e.llm != nil {
			return
		}
		e.opt.Log("Загружаю языковую модель " + e.set.LLM.Path + " ...")
		gpu := e.opt.GPULayers
		e.llm, e.llmErr = llm.NewLlama(llm.Config{Path: e.set.LLM.Path, CtxSize: e.opt.ContextSize, Threads: e.opt.Threads, GPULayers: gpu})
	})
	return e.llm, e.llmErr
}

func (e *Engine) Books() []BookMeta { return e.store.List() }

func (e *Engine) DeleteBook(id string) error { return e.store.Delete(id) }

// Book — загруженная книга.
type Book struct {
	e       *Engine
	meta    store.Meta
	tree    *chunk.Tree
	r       *rag.Retriever
	indexMu sync.Mutex
	sumMu   sync.Mutex
}

// AddBook читает документ, строит дерево разделов и фрагментов и сохраняет его.
// После возврата по книге уже работает лексический поиск и ответы; для векторного
// поиска вызовите Book.Index (можно в фоне). Повторный вызов с тем же файлом
// мгновенно открывает сохранённый индекс.
func (e *Engine) AddBook(ctx context.Context, path string, onProgress func(Progress)) (*Book, error) {
	if onProgress == nil {
		onProgress = func(Progress) {}
	}
	id, err := store.FileID(path)
	if err != nil {
		return nil, err
	}
	if e.store.Exists(id) {
		return e.OpenBook(id)
	}
	doc, err := extract.Load(ctx, path, func(d, t int) { onProgress(Progress{Stage: "extract", Done: d, Total: t}) })
	if err != nil {
		return nil, err
	}
	tree := chunk.Build(doc, chunk.Options{})
	if len(tree.Chunks) == 0 {
		return nil, extract.ErrNoText
	}
	abs, _ := filepath.Abs(path)
	meta := store.Meta{ID: id, Title: tree.Title, Author: tree.Author, Format: tree.Format, SourcePath: abs,
		Pages: tree.Pages, Sections: len(tree.Sections), Chunks: len(tree.Chunks), EmbedderID: e.emb.ID(), CreatedAt: time.Now()}
	if err := e.store.SaveBook(meta, tree); err != nil {
		return nil, err
	}
	return e.OpenBook(id)
}

func (e *Engine) OpenBook(id string) (*Book, error) {
	meta, tree, err := e.store.LoadBook(id)
	if err != nil {
		return nil, fmt.Errorf("книга не найдена: %w", err)
	}
	if meta.EmbedderID != e.emb.ID() { // сменили модель эмбеддингов → старые векторы непригодны
		_ = e.store.ResetVectors(id)
		meta.EmbedderID = e.emb.ID()
		_ = e.store.SaveMeta(meta)
	}
	vs, err := e.store.LoadVectors(id, e.emb.Dims())
	if err != nil {
		return nil, err
	}
	if vs.Len() > len(tree.Chunks) {
		vs = vec.New(e.emb.Dims())
		_ = e.store.ResetVectors(id)
	}
	return &Book{e: e, meta: meta, tree: tree, r: rag.NewRetriever(tree, e.emb, vs)}, nil
}

func (b *Book) ID() string     { return b.meta.ID }
func (b *Book) Meta() BookMeta { return b.meta }
func (b *Book) TOC() []Section { return b.tree.Sections }
func (b *Book) Chunks() int    { return len(b.tree.Chunks) }

// IndexState — сколько фрагментов уже имеют векторы.
func (b *Book) IndexState() (done, total int) { return b.r.VectorCount(), len(b.tree.Chunks) }

// Index строит (или достраивает после прерывания) векторный индекс. Блокирует до конца;
// запускайте в горутине. Прерывание через ctx безопасно — прогресс сохраняется.
func (b *Book) Index(ctx context.Context, onProgress func(Progress)) error {
	if onProgress == nil {
		onProgress = func(Progress) {}
	}
	b.indexMu.Lock()
	defer b.indexMu.Unlock()
	total := len(b.tree.Chunks)
	done := b.r.VectorCount()
	const batch = 32
	for done < total {
		if err := ctx.Err(); err != nil {
			return err
		}
		end := min(done+batch, total)
		texts := make([]string, 0, end-done)
		for _, c := range b.tree.Chunks[done:end] {
			texts = append(texts, rag.IndexText(b.tree, c))
		}
		vs, err := b.e.emb.EmbedDocs(ctx, texts)
		if err != nil {
			return err
		}
		if err := b.e.store.AppendVectors(b.meta.ID, b.e.emb.Dims(), vs); err != nil {
			return err
		}
		if err := b.r.AddVectors(vs); err != nil {
			return err
		}
		done = end
		onProgress(Progress{Stage: "embed", Done: done, Total: total})
	}
	onProgress(Progress{Stage: "ready", Done: total, Total: total})
	return nil
}

// Search — поиск фрагментов без генерации ответа.
func (b *Book) Search(ctx context.Context, query string, k int) ([]Passage, error) {
	return b.r.Search(ctx, query, rag.SearchOptions{K: k})
}

type AskRequest struct {
	ChatID   string // "" → "default"
	Question string
}

type Answer struct {
	Text    string
	Sources []Source
	ChatID  string
}

func (b *Book) answerer() (*rag.Answerer, error) {
	l, err := b.e.getLLM()
	if err != nil {
		return nil, err
	}
	return &rag.Answerer{R: b.r, LLM: l, Title: b.meta.Title, MaxContext: b.e.opt.MaxContextTokens, MaxAnswer: b.e.opt.MaxAnswerTokens}, nil
}

// Ask отвечает на вопрос по книге в контексте чата. onToken получает текст по мере генерации.
func (b *Book) Ask(ctx context.Context, req AskRequest, onToken func(string)) (*Answer, error) {
	if req.ChatID == "" {
		req.ChatID = "default"
	}
	if req.Question == "" {
		return nil, errors.New("пустой вопрос")
	}
	chat, err := b.e.store.LoadChat(b.meta.ID, req.ChatID)
	if err != nil {
		return nil, err
	}
	a, err := b.answerer()
	if err != nil {
		return nil, err
	}
	text, sources, err := a.Answer(ctx, req.Question, chat.Messages, onToken)
	if err != nil && ctx.Err() == nil {
		return nil, err
	}
	if ctx.Err() != nil && text == "" {
		return nil, ctx.Err()
	}
	now := time.Now()
	chat.Messages = append(chat.Messages,
		store.Message{Role: "user", Content: req.Question, Time: now},
		store.Message{Role: "assistant", Content: text, Sources: sources, Time: time.Now()})
	if err := b.e.store.SaveChat(b.meta.ID, chat); err != nil {
		return nil, err
	}
	return &Answer{Text: text, Sources: sources, ChatID: req.ChatID}, nil
}

func (b *Book) Chat(id string) (*Chat, error) { return b.e.store.LoadChat(b.meta.ID, id) }
func (b *Book) Chats() []string               { return b.e.store.ListChats(b.meta.ID) }
func (b *Book) DeleteChat(id string) error    { return b.e.store.DeleteChat(b.meta.ID, id) }

// SummarizeSection возвращает краткое содержание раздела (кэшируется на диске).
// sectionID = -1 — обзор всей книги.
func (b *Book) SummarizeSection(ctx context.Context, sectionID int, onToken func(string)) (string, error) {
	b.sumMu.Lock()
	defer b.sumMu.Unlock()
	cache := b.e.store.LoadSummaries(b.meta.ID)
	key := fmt.Sprint(sectionID)
	if sectionID < 0 {
		key = "book"
	}
	if s, ok := cache[key]; ok {
		if onToken != nil {
			onToken(s)
		}
		return s, nil
	}
	a, err := b.answerer()
	if err != nil {
		return "", err
	}
	var s string
	if sectionID < 0 {
		s, err = a.SummarizeBook(ctx, cache, onToken)
	} else {
		s, err = a.SummarizeSection(ctx, sectionID, onToken)
	}
	if err != nil {
		return "", err
	}
	cache[key] = s
	_ = b.e.store.SaveSummaries(b.meta.ID, cache)
	return s, nil
}
