// Package chunk строит иерархию «книга → разделы → фрагменты» из документа.
package chunk

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/tnpknhghzc/locallm/internal/extract"
)

type Section struct {
	ID        int    `json:"id"`
	Title     string `json:"title"`
	Path      string `json:"path"` // «Часть I › Глава 2 › 2.1»
	Level     int    `json:"level"`
	Parent    int    `json:"parent"` // -1 у корневых
	PageFrom  int    `json:"page_from"`
	PageTo    int    `json:"page_to"`
	ChunkFrom int    `json:"chunk_from"` // диапазон собственных чанков [from,to)
	ChunkTo   int    `json:"chunk_to"`
}

type Chunk struct {
	ID       int    `json:"id"`
	Section  int    `json:"section"`
	PageFrom int    `json:"page_from"`
	PageTo   int    `json:"page_to"`
	Text     string `json:"text"`
}

type Tree struct {
	Title    string    `json:"title"`
	Author   string    `json:"author"`
	Format   string    `json:"format"`
	Pages    int       `json:"pages"`
	Sections []Section `json:"sections"`
	Chunks   []Chunk   `json:"chunks"`
}

type Options struct {
	TargetChars  int // целевой размер фрагмента
	MaxChars     int // жёсткий максимум
	OverlapChars int
	WindowPages  int // размер «искусственного раздела», если в документе нет заголовков
}

func (o *Options) defaults() {
	if o.TargetChars == 0 {
		o.TargetChars = 1000
	}
	if o.MaxChars == 0 {
		o.MaxChars = 1400
	}
	if o.OverlapChars == 0 {
		o.OverlapChars = 160
	}
	if o.WindowPages == 0 {
		o.WindowPages = 20
	}
}

// Build строит дерево разделов и фрагментов.
func Build(doc *extract.Document, opt Options) *Tree {
	opt.defaults()
	t := &Tree{Title: doc.Title, Author: doc.Author, Format: doc.Format, Pages: len(doc.Pages)}
	heads := normalizeHeadings(doc.Headings, len(doc.Pages))
	if len(heads) < 2 {
		heads = nil
		for p := 1; p <= len(doc.Pages); p += opt.WindowPages {
			to := min(p+opt.WindowPages-1, len(doc.Pages))
			heads = append(heads, extract.Heading{Level: 1, Title: fmt.Sprintf("Стр. %d–%d", p, to), Page: p})
		}
	} else if heads[0].Page > 1 {
		heads = append([]extract.Heading{{Level: heads[0].Level, Title: "Начало", Page: 1}}, heads...)
	}
	// иерархия
	var stack []int
	for i, h := range heads {
		for len(stack) > 0 && t.Sections[stack[len(stack)-1]].Level >= h.Level {
			stack = stack[:len(stack)-1]
		}
		parent, path := -1, h.Title
		if len(stack) > 0 {
			parent = stack[len(stack)-1]
			path = t.Sections[parent].Path + " › " + h.Title
		}
		t.Sections = append(t.Sections, Section{ID: i, Title: h.Title, Path: path, Level: h.Level, Parent: parent, PageFrom: h.Page, PageTo: h.Page})
		stack = append(stack, i)
	}
	// страница → раздел-владелец (последний заголовок со страницей ≤ p)
	owner := make([]int, len(doc.Pages)+2)
	si := 0
	for p := 1; p <= len(doc.Pages); p++ {
		for si+1 < len(heads) && heads[si+1].Page <= p {
			si++
		}
		owner[p] = si
	}
	for i := range t.Sections {
		t.Sections[i].PageTo = t.Sections[i].PageFrom - 1
	}
	for p := 1; p <= len(doc.Pages); p++ {
		s := &t.Sections[owner[p]]
		s.PageTo = p
	}
	// чанки по разделам
	for si := range t.Sections {
		s := &t.Sections[si]
		s.ChunkFrom = len(t.Chunks)
		if s.PageTo >= s.PageFrom {
			var paras []para
			for p := s.PageFrom; p <= s.PageTo; p++ {
				if owner[p] != si {
					continue
				}
				for _, x := range strings.Split(doc.Pages[p-1], "\n\n") {
					if x = strings.TrimSpace(x); x != "" {
						paras = append(paras, para{x, p})
					}
				}
			}
			for _, c := range split(paras, opt) {
				c.ID = len(t.Chunks)
				c.Section = si
				t.Chunks = append(t.Chunks, c)
			}
		}
		s.ChunkTo = len(t.Chunks)
	}
	// у родителей расширяем диапазон страниц на потомков
	for i := len(t.Sections) - 1; i >= 0; i-- {
		s := t.Sections[i]
		if s.Parent >= 0 {
			p := &t.Sections[s.Parent]
			if s.PageTo > p.PageTo {
				p.PageTo = s.PageTo
			}
		}
	}
	return t
}

func normalizeHeadings(hs []extract.Heading, pages int) []extract.Heading {
	var out []extract.Heading
	for _, h := range hs {
		if h.Page < 1 || h.Page > pages || strings.TrimSpace(h.Title) == "" {
			continue
		}
		if h.Level < 1 {
			h.Level = 1
		}
		out = append(out, h)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Page < out[j].Page })
	return out
}

type para struct {
	text string
	page int
}

type piece struct {
	text string
	page int
}

func split(paras []para, o Options) []Chunk {
	var pieces []piece
	for _, p := range paras {
		if utf8.RuneCountInString(p.text) <= o.MaxChars {
			pieces = append(pieces, piece{p.text, p.page})
			continue
		}
		for _, s := range Sentences(p.text) {
			for _, part := range hardSplit(s, o.MaxChars) {
				pieces = append(pieces, piece{part, p.page})
			}
		}
	}
	var chunks []Chunk
	var cur []piece
	curLen := 0
	emit := func() {
		if len(cur) == 0 {
			return
		}
		var sb strings.Builder
		for i, p := range cur {
			if i > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString(p.text)
		}
		chunks = append(chunks, Chunk{PageFrom: cur[0].page, PageTo: cur[len(cur)-1].page, Text: sb.String()})
		// перекрытие: хвост из последних кусков до OverlapChars
		var tail []piece
		l := 0
		for i := len(cur) - 1; i >= 1; i-- {
			n := utf8.RuneCountInString(cur[i].text)
			if l+n > o.OverlapChars {
				break
			}
			tail = append([]piece{cur[i]}, tail...)
			l += n
		}
		cur, curLen = tail, l
	}
	for _, p := range pieces {
		n := utf8.RuneCountInString(p.text)
		if curLen+n > o.TargetChars && curLen > 0 {
			emit()
		}
		cur = append(cur, p)
		curLen += n + 1
	}
	if len(cur) > 0 {
		last := Chunk{}
		var sb strings.Builder
		for i, p := range cur {
			if i > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString(p.text)
		}
		last = Chunk{PageFrom: cur[0].page, PageTo: cur[len(cur)-1].page, Text: sb.String()}
		// хвост, целиком состоящий из перекрытия, не нужен
		if len(chunks) == 0 || !strings.Contains(chunks[len(chunks)-1].Text, last.Text) {
			chunks = append(chunks, last)
		}
	}
	return chunks
}

func hardSplit(s string, max int) []string {
	if utf8.RuneCountInString(s) <= max {
		return []string{s}
	}
	var out []string
	var cur strings.Builder
	n := 0
	for _, w := range strings.Fields(s) {
		wl := utf8.RuneCountInString(w) + 1
		if n+wl > max && n > 0 {
			out = append(out, cur.String())
			cur.Reset()
			n = 0
		}
		if n > 0 {
			cur.WriteByte(' ')
		}
		cur.WriteString(w)
		n += wl
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

var abbr = map[string]bool{"г": true, "гг": true, "т": true, "д": true, "п": true, "в": true, "вв": true, "см": true, "рис": true, "стр": true,
	"табл": true, "им": true, "ул": true, "пр": true, "др": true, "проф": true, "акад": true, "тыс": true, "млн": true, "млрд": true,
	"руб": true, "коп": true, "ср": true, "напр": true, "англ": true, "нем": true, "лат": true, "etc": true, "vs": true, "mr": true,
	"mrs": true, "dr": true, "fig": true, "no": true, "ок": true, "обл": true, "гл": true}

// Sentences делит текст на предложения с учётом русских сокращений и инициалов.
func Sentences(text string) []string {
	rs := []rune(text)
	var out []string
	start := 0
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		if r != '.' && r != '!' && r != '?' && r != '…' {
			continue
		}
		j := i + 1
		for j < len(rs) && (rs[j] == '.' || rs[j] == '!' || rs[j] == '?' || rs[j] == '»' || rs[j] == '"' || rs[j] == '”' || rs[j] == ')') {
			j++
		}
		if j >= len(rs) {
			break
		}
		if !unicode.IsSpace(rs[j]) {
			continue
		}
		k := j
		for k < len(rs) && unicode.IsSpace(rs[k]) {
			k++
		}
		if k >= len(rs) {
			break
		}
		next := rs[k]
		if !(unicode.IsUpper(next) || unicode.IsDigit(next) || next == '«' || next == '"' || next == '—' || next == '–' || next == '„' || next == '“') {
			continue
		}
		if r == '.' {
			// слово перед точкой
			b := i
			for b > start && unicode.IsLetter(rs[b-1]) {
				b--
			}
			w := strings.ToLower(string(rs[b:i]))
			if abbr[w] || (utf8.RuneCountInString(w) == 1 && unicode.IsUpper(rs[b])) {
				continue
			}
		}
		out = append(out, strings.TrimSpace(string(rs[start:j])))
		start = k
		i = k - 1
	}
	if s := strings.TrimSpace(string(rs[start:])); s != "" {
		out = append(out, s)
	}
	return out
}
