// Package llm — генерация текста локальной моделью.
package llm

import "context"

type Message struct{ Role, Content string }

type Options struct {
	MaxTokens   int
	Temperature float32
}

type LLM interface {
	Name() string
	ContextSize() int
	CountTokens(text string) int
	// Chat генерирует ответ; onToken (может быть nil) получает куски текста по мере генерации.
	Chat(ctx context.Context, msgs []Message, opt Options, onToken func(string)) (string, error)
	Close()
}

// ApproxTokens — запасная оценка числа токенов (русский ≈ 2.3 символа/токен).
func ApproxTokens(s string) int {
	n := 0
	for range s {
		n++
	}
	return n*10/23 + 1
}
