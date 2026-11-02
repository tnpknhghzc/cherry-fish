package locallm

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/tnpknhghzc/locallm/internal/embed"
	"github.com/tnpknhghzc/locallm/internal/llm"
)

// Замер на большой книге: LOCALLM_BIG_PDF=/path/big.pdf go test -run TestBigBook -v
func TestBigBook(t *testing.T) {
	p := os.Getenv("LOCALLM_BIG_PDF")
	if p == "" {
		t.Skip("LOCALLM_BIG_PDF не задан")
	}
	e, _ := Open(Options{DataDir: t.TempDir(), LLM: &llm.Fake{}, Embedder: embed.NewHash(1024)})
	defer e.Close()
	t0 := time.Now()
	b, err := e.AddBook(context.Background(), p, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("чтение PDF + дерево + сохранение: %v (%d стр., %d фрагментов)", time.Since(t0), b.Meta().Pages, b.Chunks())
	t0 = time.Now()
	b.Index(context.Background(), nil)
	t.Logf("векторы (hash, dims=1024): %v", time.Since(t0))
	t0 = time.Now()
	e2, _ := Open(Options{DataDir: e.opt.DataDir, LLM: &llm.Fake{}, Embedder: embed.NewHash(1024)})
	b2, _ := e2.OpenBook(b.ID())
	t.Logf("повторное открытие книги (BM25+векторы с диска): %v", time.Since(t0))
	t0 = time.Now()
	ps, _ := b2.Search(context.Background(), "особенность код-777 в главе 6", 6)
	t.Logf("поиск: %v, найдено %d, первый: стр. %d", time.Since(t0), len(ps), ps[0].PageFrom)
}
