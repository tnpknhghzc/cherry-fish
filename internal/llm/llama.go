//go:build !nollama

package llm

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	llama "github.com/tcpipuk/llama-go"
)

type Llama struct {
	mu    sync.Mutex
	model *llama.Model
	ctx   *llama.Context
	name  string
	nctx  int
}

type Config struct {
	Path      string
	CtxSize   int
	Threads   int
	GPULayers int
}

// NewLlama загружает модель и создаёт ОДИН контекст на всё время работы:
// автоматический prefix-cache llama.cpp повторно использует системный
// промпт и историю чата между запросами.
func NewLlama(c Config) (*Llama, error) {
	m, err := llama.LoadModel(c.Path, llama.WithGPULayers(c.GPULayers), llama.WithMMap(true), llama.WithSilentLoading())
	if err != nil {
		return nil, fmt.Errorf("загрузка языковой модели: %w", err)
	}
	mk := func(extra ...llama.ContextOption) (*llama.Context, error) {
		return m.NewContext(append([]llama.ContextOption{
			llama.WithContext(c.CtxSize),
			llama.WithBatch(512),
			llama.WithThreads(c.Threads),
			llama.WithThreadsBatch(c.Threads),
			llama.WithPrefixCaching(true),
		}, extra...)...)
	}
	cx, err := mk()
	if err != nil { // некоторые модели не поддерживают квантованный KV-кэш по умолчанию
		cx, err = mk(llama.WithKVCacheType("f16"))
	}
	if err != nil {
		m.Close()
		return nil, fmt.Errorf("контекст языковой модели: %w", err)
	}
	return &Llama{model: m, ctx: cx, name: filepath.Base(c.Path), nctx: c.CtxSize}, nil
}

func (l *Llama) Name() string     { return l.name }
func (l *Llama) ContextSize() int { return l.nctx }

func (l *Llama) CountTokens(text string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.ctx == nil {
		return ApproxTokens(text)
	}
	t, err := l.ctx.Tokenize(text)
	if err != nil {
		return ApproxTokens(text)
	}
	return len(t)
}

func (l *Llama) Chat(ctx context.Context, msgs []Message, opt Options, onToken func(string)) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.ctx == nil {
		return "", fmt.Errorf("модель закрыта")
	}
	cm := make([]llama.ChatMessage, len(msgs))
	for i, m := range msgs {
		cm[i] = llama.ChatMessage{Role: m.Role, Content: m.Content}
	}
	temp := opt.Temperature
	topP, topK := float32(0.9), 40
	co := llama.ChatOptions{
		MaxTokens:   llama.Int(max(opt.MaxTokens, 16)),
		Temperature: &temp,
		TopP:        &topP,
		TopK:        &topK,
		StopWords:   []string{"<|im_end|>", "<|endoftext|>"},
	}
	deltas, errs := l.ctx.ChatStream(ctx, cm, co)
	var sb strings.Builder
	var rerr error
	for deltas != nil || errs != nil {
		select {
		case d, ok := <-deltas:
			if !ok {
				deltas = nil
				continue
			}
			if d.Content != "" {
				sb.WriteString(d.Content)
				if onToken != nil {
					onToken(d.Content)
				}
			}
		case e, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			if e != nil {
				rerr = e
			}
		}
	}
	if rerr != nil && ctx.Err() == nil {
		return sb.String(), rerr
	}
	if ctx.Err() != nil {
		return sb.String(), ctx.Err()
	}
	return strings.TrimSpace(sb.String()), nil
}

func (l *Llama) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.ctx != nil {
		l.ctx.Close()
		l.ctx = nil
	}
	if l.model != nil {
		l.model.Close()
		l.model = nil
	}
}
