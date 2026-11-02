// Package embed — векторизация текста.
package embed

import (
	"context"
	"hash/fnv"
	"strings"

	"github.com/tnpknhghzc/locallm/internal/textproc"
	"github.com/tnpknhghzc/locallm/internal/vec"
)

type Embedder interface {
	ID() string // меняется при смене модели → индекс пересчитывается
	Dims() int
	EmbedDocs(ctx context.Context, texts []string) ([][]float32, error)
	EmbedQuery(ctx context.Context, text string) ([]float32, error)
	Close()
}

// MaxChars — ограничение длины текста, чтобы не превысить окно модели.
const MaxChars = 1600

func clip(s string) string {
	r := []rune(s)
	if len(r) > MaxChars {
		return string(r[:MaxChars])
	}
	return s
}

// Hash — детерминированный «мешок слов» через feature hashing. Не требует модели;
// используется в тестах и как запасной вариант. Семантики не понимает.
type Hash struct{ dims int }

func NewHash(dims int) *Hash {
	if dims <= 0 {
		dims = 256
	}
	return &Hash{dims}
}

func (h *Hash) ID() string { return "hash-v1" }
func (h *Hash) Dims() int  { return h.dims }
func (h *Hash) Close()     {}

func (h *Hash) one(text string) []float32 {
	v := make([]float32, h.dims)
	for _, t := range textproc.Tokens(text) {
		f := fnv.New32a()
		f.Write([]byte(strings.ToLower(t)))
		s := f.Sum32()
		sign := float32(1)
		if s&0x80000000 != 0 {
			sign = -1
		}
		v[int(s)%h.dims] += sign
	}
	vec.Normalize(v)
	return v
}

func (h *Hash) EmbedDocs(ctx context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		out[i] = h.one(t)
	}
	return out, nil
}

func (h *Hash) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	return h.one(text), nil
}
