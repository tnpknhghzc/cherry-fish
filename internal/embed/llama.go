//go:build !nollama

package embed

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	llama "github.com/tcpipuk/llama-go"
	"github.com/tnpknhghzc/locallm/internal/vec"
)

type Llama struct {
	mu         sync.Mutex
	model      *llama.Model
	ctx        *llama.Context
	dims       int
	id         string
	qPre, dPre string
	batchTexts int
}

// NewLlama загружает embedding-модель (GGUF) и один раз создаёт контекст,
// который переиспользуется для всех пакетов (в исходной версии контекст
// создавался на КАЖДЫЙ фрагмент — это было главным тормозом индексации).
func NewLlama(path string, threads int, queryPrefix, docPrefix string) (*Llama, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	m, err := llama.LoadModel(path, llama.WithGPULayers(0), llama.WithMMap(true), llama.WithSilentLoading())
	if err != nil {
		return nil, fmt.Errorf("загрузка модели эмбеддингов: %w", err)
	}
	const par = 4
	c, err := m.NewContext(
		llama.WithEmbeddings(),
		llama.WithContext(2048),
		llama.WithBatch(2048),
		llama.WithUBatch(2048),
		llama.WithParallel(par),
		llama.WithThreads(threads),
		llama.WithThreadsBatch(threads),
		llama.WithKVCacheType("f16"),
	)
	if err != nil {
		m.Close()
		return nil, fmt.Errorf("контекст эмбеддингов: %w", err)
	}
	e := &Llama{model: m, ctx: c, qPre: queryPrefix, dPre: docPrefix, batchTexts: par * 4,
		id: fmt.Sprintf("%s:%d", filepath.Base(path), st.Size())}
	probe, err := c.GetEmbeddingsBatch([]string{"проба"})
	if err != nil {
		e.Close()
		return nil, fmt.Errorf("проверка модели эмбеддингов: %w", err)
	}
	e.dims = len(probe[0])
	return e, nil
}

// maxTok — предел токенов на одну последовательность: контекст 2048 делится на 4
// параллельные последовательности (по 512), а у многих моделей (BERT/e5) и позиций всего 512.
const maxTok = 496

// fit обрезает текст так, чтобы он гарантированно уместился в maxTok токенов.
// Вызывается под e.mu.
func (e *Llama) fit(s string) string {
	s = clip(s)
	for i := 0; i < 4; i++ {
		toks, err := e.ctx.Tokenize(s)
		if err != nil || len(toks) <= maxTok {
			return s
		}
		r := []rune(s)
		keep := int(float64(len(r)) * float64(maxTok) / float64(len(toks)) * 0.95)
		if keep < 1 || keep >= len(r) {
			return s
		}
		s = string(r[:keep])
	}
	return s
}

func (e *Llama) ID() string { return e.id }
func (e *Llama) Dims() int  { return e.dims }

func (e *Llama) EmbedDocs(ctx context.Context, texts []string) ([][]float32, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([][]float32, 0, len(texts))
	for i := 0; i < len(texts); i += e.batchTexts {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		end := min(i+e.batchTexts, len(texts))
		in := make([]string, 0, end-i)
		for _, t := range texts[i:end] {
			in = append(in, e.fit(e.dPre+t))
		}
		vs, err := e.ctx.GetEmbeddingsBatch(in)
		if err != nil {
			return nil, err
		}
		for _, v := range vs {
			vec.Normalize(v)
			out = append(out, v)
		}
	}
	return out, nil
}

func (e *Llama) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	vs, err := e.ctx.GetEmbeddingsBatch([]string{e.fit(e.qPre + text)})
	if err != nil {
		return nil, err
	}
	vec.Normalize(vs[0])
	return vs[0], nil
}

func (e *Llama) Close() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.ctx != nil {
		e.ctx.Close()
		e.ctx = nil
	}
	if e.model != nil {
		e.model.Close()
		e.model = nil
	}
}
