package chunk

import (
	"fmt"
	"strings"
	"testing"

	"github.com/tnpknhghzc/locallm/internal/extract"
)

func TestSentences(t *testing.T) {
	s := Sentences("А. С. Пушкин родился в 1799 г. в Москве. Он писал стихи! Правда? Да.")
	if len(s) != 4 {
		t.Fatalf("%d: %q", len(s), s)
	}
}

func mkDoc(pages int) *extract.Document {
	d := &extract.Document{Title: "T"}
	for i := 1; i <= pages; i++ {
		d.Pages = append(d.Pages, strings.Repeat(fmt.Sprintf("Предложение номер %d на странице. ", i), 40)+"\n\n"+strings.Repeat("Ещё один абзац текста. ", 30))
	}
	return d
}

func TestTreeWithHeadings(t *testing.T) {
	d := mkDoc(30)
	d.Headings = []extract.Heading{{Level: 1, Title: "Часть I", Page: 1}, {Level: 2, Title: "Глава 1", Page: 1}, {Level: 2, Title: "Глава 2", Page: 10}, {Level: 1, Title: "Часть II", Page: 20}, {Level: 2, Title: "Глава 3", Page: 21}}
	tr := Build(d, Options{})
	if len(tr.Sections) != 5 {
		t.Fatalf("разделов: %d", len(tr.Sections))
	}
	if tr.Sections[2].Parent != 0 || tr.Sections[2].Path != "Часть I › Глава 2" {
		t.Fatalf("%+v", tr.Sections[2])
	}
	if tr.Sections[0].ChunkTo != tr.Sections[0].ChunkFrom { // «Часть I» и «Глава 1» на одной странице → текст у главы
		t.Fatalf("у Части I не должно быть собственных чанков: %+v", tr.Sections[0])
	}
	for _, c := range tr.Chunks {
		s := tr.Sections[c.Section]
		if c.PageFrom < s.PageFrom || c.PageTo > s.PageTo {
			t.Fatalf("страницы чанка вне раздела: %+v %+v", c, s)
		}
		if len([]rune(c.Text)) > 1500 {
			t.Fatalf("слишком длинный чанк: %d", len([]rune(c.Text)))
		}
	}
	if len(tr.Chunks) < 30 {
		t.Fatalf("слишком мало чанков: %d", len(tr.Chunks))
	}
}

func TestTreeWindows(t *testing.T) {
	tr := Build(mkDoc(45), Options{WindowPages: 20})
	if len(tr.Sections) != 3 || tr.Sections[2].Title != "Стр. 41–45" {
		t.Fatalf("%+v", tr.Sections)
	}
}
