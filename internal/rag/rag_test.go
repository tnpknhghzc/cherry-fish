package rag

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/tnpknhghzc/locallm/internal/chunk"
	"github.com/tnpknhghzc/locallm/internal/embed"
	"github.com/tnpknhghzc/locallm/internal/extract"
	"github.com/tnpknhghzc/locallm/internal/llm"
	"github.com/tnpknhghzc/locallm/internal/store"
	"github.com/tnpknhghzc/locallm/internal/vec"
)

func testBook() *chunk.Tree {
	d := &extract.Document{Title: "Тест"}
	topics := []string{
		"Керлинг — командная игра, в которой спортсмены скользят гранитные камни по льду к мишени дома.",
		"Фотосинтез — процесс преобразования световой энергии в химическую в хлоропластах растений.",
		"Римская империя пала в пятом веке после вторжений варварских племён и внутренних кризисов.",
		"Нейронные сети обучаются методом обратного распространения ошибки и градиентного спуска.",
	}
	for i := 0; i < 40; i++ {
		d.Pages = append(d.Pages, strings.Repeat(fmt.Sprintf("Общий вводный текст страницы %d без особых тем. ", i), 20)+"\n\n"+strings.Repeat(topics[i/10]+" ", 8))
	}
	d.Headings = []extract.Heading{{Level: 1, Title: "Глава 1. Спорт", Page: 1}, {Level: 1, Title: "Глава 2. Биология", Page: 11}, {Level: 1, Title: "Глава 3. История", Page: 21}, {Level: 1, Title: "Глава 4. Информатика", Page: 31}}
	return chunk.Build(d, chunk.Options{})
}

func newRetr(t *testing.T, withVec bool) *Retriever {
	tr := testBook()
	emb := embed.NewHash(512)
	v := vec.New(emb.Dims())
	r := NewRetriever(tr, emb, v)
	if withVec {
		texts := make([]string, len(tr.Chunks))
		for i, c := range tr.Chunks {
			texts[i] = IndexText(tr, c)
		}
		vs, _ := emb.EmbedDocs(context.Background(), texts)
		r.AddVectors(vs)
	}
	return r
}

func TestSearchFindsRightChapter(t *testing.T) {
	for _, withVec := range []bool{false, true} {
		r := newRetr(t, withVec)
		ps, err := r.Search(context.Background(), "как обучаются нейронные сети градиентный спуск", SearchOptions{K: 4})
		if err != nil || len(ps) == 0 {
			t.Fatal(err, ps)
		}
		if !strings.Contains(ps[0].Section, "Информатика") {
			t.Fatalf("vec=%v: найден раздел %q", withVec, ps[0].Section)
		}
	}
}

func TestMorphology(t *testing.T) {
	r := newRetr(t, false)
	// «керлингом»/«камнями» должны находить «керлинг»/«камни»
	ps, _ := r.Search(context.Background(), "что делают с камнями в керлинге", SearchOptions{K: 3})
	if len(ps) == 0 || !strings.Contains(ps[0].Section, "Спорт") {
		t.Fatalf("%+v", ps)
	}
}

func TestChapterReference(t *testing.T) {
	r := newRetr(t, true)
	ps, _ := r.Search(context.Background(), "что написано в главе 3 про империю", SearchOptions{K: 3})
	for _, p := range ps {
		if !strings.Contains(p.Section, "История") {
			t.Fatalf("вышли за пределы главы 3: %q", p.Section)
		}
	}
	if sid, ok := r.sectionByReference("в главе IV"); !ok || r.Tree.Sections[sid].Title != "Глава 4. Информатика" {
		t.Fatal("римские цифры не распознаны")
	}
}

func TestAnswerUsesHistoryAndSources(t *testing.T) {
	r := newRetr(t, true)
	f := &llm.Fake{}
	a := &Answerer{R: r, LLM: f, Title: "Тест"}
	ans, src, err := a.Answer(context.Background(), "А как работает фотосинтез?", []store.Message{{Role: "user", Content: "привет"}, {Role: "assistant", Content: "здравствуйте"}}, nil)
	if err != nil || ans == "" || len(src) == 0 {
		t.Fatal(err, ans, src)
	}
	if f.Last[0].Role != "system" || len(f.Last) < 4 {
		t.Fatalf("история не передана: %+v", f.Last)
	}
	if !strings.Contains(f.Last[len(f.Last)-1].Content, "[1]") {
		t.Fatal("в промпте нет нумерованных фрагментов")
	}
}

func TestNoInfo(t *testing.T) {
	r := newRetr(t, false)
	a := &Answerer{R: r, LLM: &llm.Fake{}, Title: "T"}
	ans, src, _ := a.Answer(context.Background(), "квантовая хромодинамика", nil, nil)
	if ans != NoInfoRU || src != nil {
		t.Fatalf("%q", ans)
	}
}
