package llm

import (
	"context"
	"strings"
)

// Fake — заглушка для тестов: возвращает последний «user»-запрос, обрезанный.
type Fake struct{ Last []Message }

func (f *Fake) Name() string             { return "fake" }
func (f *Fake) ContextSize() int         { return 4096 }
func (f *Fake) CountTokens(s string) int { return ApproxTokens(s) }
func (f *Fake) Close()                   {}
func (f *Fake) Chat(ctx context.Context, msgs []Message, opt Options, on func(string)) (string, error) {
	f.Last = msgs
	out := "ОТВЕТ: " + strings.TrimSpace(msgs[len(msgs)-1].Content)
	if r := []rune(out); len(r) > 200 {
		out = string(r[:200])
	}
	if on != nil {
		on(out)
	}
	return out, ctx.Err()
}
