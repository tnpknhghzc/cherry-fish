package locallm

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tnpknhghzc/locallm/internal/embed"
	"github.com/tnpknhghzc/locallm/internal/llm"
)

func writeBook(t *testing.T) string {
	var sb strings.Builder
	chapters := []struct{ name, text string }{
		{"Глава 1. Керлинг", "Керлинг — командная игра: спортсмены скользят гранитные камни по льду к мишени."},
		{"Глава 2. Растения", "Фотосинтез — преобразование света в химическую энергию в хлоропластах листьев."},
		{"Глава 3. Сети", "Нейронные сети обучаются обратным распространением ошибки и градиентным спуском."},
	}
	for _, c := range chapters {
		sb.WriteString("# " + c.name + "\n\n")
		for i := 0; i < 600; i++ {
			sb.WriteString(fmt.Sprintf("%s Абзац номер %d, дополнительный шум без смысла.\n\n", c.text, i))
		}
	}
	p := filepath.Join(t.TempDir(), "book.md")
	os.WriteFile(p, []byte(sb.String()), 0o644)
	return p
}

func TestEndToEnd(t *testing.T) {
	e, err := Open(Options{DataDir: t.TempDir(), LLM: &llm.Fake{}, Embedder: embed.NewHash(256)})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	p := writeBook(t)
	var stages []string
	b, err := e.AddBook(context.Background(), p, func(pr Progress) { stages = append(stages, pr.Stage) })
	if err != nil {
		t.Fatal(err)
	}
	if len(b.TOC()) != 3 {
		t.Fatalf("разделов: %d", len(b.TOC()))
	}
	// до индексации векторов поиск уже работает (BM25)
	ans, err := b.Ask(context.Background(), AskRequest{Question: "Как обучаются нейронные сети?"}, nil)
	if err != nil || len(ans.Sources) == 0 || !strings.Contains(ans.Sources[0].Section, "Сети") {
		t.Fatalf("%v %+v", err, ans)
	}
	// индексация с прерыванием и продолжением
	ctx, cancel := context.WithCancel(context.Background())
	n := 0
	err = b.Index(ctx, func(Progress) {
		if n++; n == 2 {
			cancel()
		}
	})
	if err == nil {
		t.Fatal("ожидалось прерывание")
	}
	d1, total := b.IndexState()
	if d1 == 0 || d1 >= total {
		t.Fatalf("частичный прогресс: %d/%d", d1, total)
	}
	b2, err := e.AddBook(context.Background(), p, nil) // «перезапуск»: индекс подхватывается с диска
	if err != nil {
		t.Fatal(err)
	}
	if d2, _ := b2.IndexState(); d2 != d1 {
		t.Fatalf("после перезапуска векторов %d, было %d", d2, d1)
	}
	if err := b2.Index(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if d, tot := b2.IndexState(); d != tot {
		t.Fatalf("%d/%d", d, tot)
	}
	// чат помнит историю
	if _, err := b2.Ask(context.Background(), AskRequest{ChatID: "c1", Question: "Что такое фотосинтез?"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := b2.Ask(context.Background(), AskRequest{ChatID: "c1", Question: "А подробнее?"}, nil); err != nil {
		t.Fatal(err)
	}
	c, _ := b2.Chat("c1")
	if len(c.Messages) != 4 {
		t.Fatalf("сообщений в чате: %d", len(c.Messages))
	}
	s, err := b2.SummarizeSection(context.Background(), 1, nil)
	if err != nil || s == "" {
		t.Fatal(err)
	}
	if _, err := b2.SummarizeSection(context.Background(), -1, nil); err != nil {
		t.Fatal(err)
	}
}
