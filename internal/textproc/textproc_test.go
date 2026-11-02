package textproc

import "testing"

func TestStemRU(t *testing.T) {
	groups := [][]string{
		{"книга", "книги", "книгу", "книге", "книгой"},
		{"глава", "главы", "главе", "главу"},
		{"обучение", "обучения", "обучению"},
		{"нейронные", "нейронных", "нейронными"},
		{"камни", "камней", "камнями"},
	}
	for _, g := range groups {
		base := StemRU(g[0])
		for _, w := range g[1:] {
			if s := StemRU(w); s != base {
				t.Errorf("%q → %q, ожидалось %q (как у %q)", w, s, base, g[0])
			}
		}
	}
	if StemRU("керлинг") == StemRU("игра") {
		t.Error("разные слова склеились")
	}
}

func TestTokensMixed(t *testing.T) {
	got := Tokens("Что такое Backpropagation? Ёлка, 2024 год")
	want := map[string]bool{"backpropagation": true, "елк": true, "2024": true}
	found := 0
	for _, g := range got {
		if want[g] {
			found++
		}
	}
	if found < 2 {
		t.Fatalf("%v", got)
	}
}

func TestBM25(t *testing.T) {
	x := NewBM25()
	x.Add("Керлинг — игра на льду с камнями")
	x.Add("Фотосинтез происходит в листьях растений")
	x.Add("Нейронные сети и обучение")
	x.Finish()
	h := x.Search("камнями для керлинга", 3, nil)
	if len(h) == 0 || h[0].Doc != 0 {
		t.Fatalf("%+v", h)
	}
	if h2 := x.Search("камни", 3, func(d int) bool { return d != 0 }); len(h2) != 0 {
		t.Fatalf("allow-фильтр не работает: %+v", h2)
	}
}
