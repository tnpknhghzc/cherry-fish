//go:build !nollama

package locallm

import (
	"context"
	"os"
	"testing"
)

// Интеграционный тест с НАСТОЯЩИМ llama.cpp. Запускается, только если задана
// LOCALLM_TEST_MODELS (папка с .gguf; подойдут крошечные из scripts/make_test_models.py).
func TestRealLlamaPipeline(t *testing.T) {
	dir := os.Getenv("LOCALLM_TEST_MODELS")
	if dir == "" {
		t.Skip("LOCALLM_TEST_MODELS не задан")
	}
	e, err := Open(Options{DataDir: t.TempDir(), ModelsDir: dir, Threads: 2, ContextSize: 2048})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	b, err := e.AddBook(context.Background(), writeBook(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Index(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	var got int
	ans, err := b.Ask(context.Background(), AskRequest{Question: "Что такое фотосинтез?"}, func(s string) { got += len(s) })
	if err != nil || got == 0 || len(ans.Sources) == 0 {
		t.Fatalf("%v tokens=%d %+v", err, got, ans)
	}
	// отмена посреди генерации
	ctx, cancel := context.WithCancel(context.Background())
	n := 0
	_, err = b.Ask(ctx, AskRequest{Question: "Расскажи подробно про керлинг"}, func(string) {
		if n++; n == 3 {
			cancel()
		}
	})
	if err == nil && n < 3 {
		t.Fatal("генерация не стримится")
	}
}
