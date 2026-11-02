// Package rag — гибридный поиск по иерархическому индексу книги и сборка промпта.
//
// Поиск: BM25 (со стеммингом) + векторный поиск → слияние RRF → бонус разделам,
// чьи «центроиды» ближе всего к вопросу (уровень дерева) → точная ссылка на
// главу («глава 3») сужает поиск до её поддерева → слияние соседних фрагментов.
package rag

import (
	"context"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/tnpknhghzc/locallm/internal/chunk"
	"github.com/tnpknhghzc/locallm/internal/embed"
	"github.com/tnpknhghzc/locallm/internal/textproc"
	"github.com/tnpknhghzc/locallm/internal/vec"
)

type Retriever struct {
	Tree *chunk.Tree
	Emb  embed.Embedder

	mu        sync.RWMutex
	bm25      *textproc.BM25
	vecs      *vec.Index
	centroids [][]float32
	centAt    int // при скольких векторах посчитаны центроиды
	subtree   [][]int
}

// IndexText — текст фрагмента для индексации: заголовок раздела + сам текст.
func IndexText(t *chunk.Tree, c chunk.Chunk) string {
	return t.Sections[c.Section].Title + ". " + c.Text
}

func NewRetriever(t *chunk.Tree, emb embed.Embedder, vecs *vec.Index) *Retriever {
	r := &Retriever{Tree: t, Emb: emb, vecs: vecs, bm25: textproc.NewBM25()}
	for _, c := range t.Chunks {
		r.bm25.Add(IndexText(t, c))
	}
	r.bm25.Finish()
	r.subtree = make([][]int, len(t.Sections))
	for i := range t.Sections { // раздел и все потомки (потомки всегда идут после родителя)
		r.subtree[i] = append(r.subtree[i], i)
		for p := t.Sections[i].Parent; p >= 0; p = t.Sections[p].Parent {
			r.subtree[p] = append(r.subtree[p], i)
		}
	}
	return r
}

// AddVectors дописывает векторы следующих фрагментов (в порядке id).
func (r *Retriever) AddVectors(vs [][]float32) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.vecs.Add(vs...)
}

func (r *Retriever) VectorCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.vecs.Len()
}

func (r *Retriever) computeCentroids() {
	n := r.vecs.Len()
	if r.centroids != nil && r.centAt == n {
		return
	}
	d := r.vecs.Dims()
	cs := make([][]float32, len(r.Tree.Sections))
	for si := range r.Tree.Sections {
		var sum []float32
		cnt := 0
		for _, sub := range r.subtree[si] {
			s := r.Tree.Sections[sub]
			for c := s.ChunkFrom; c < s.ChunkTo && c < n; c++ {
				if sum == nil {
					sum = make([]float32, d)
				}
				v := r.vecs.Vector(c)
				for i := range sum {
					sum[i] += v[i]
				}
				cnt++
			}
		}
		if cnt > 0 {
			vec.Normalize(sum)
			cs[si] = sum
		}
	}
	r.centroids, r.centAt = cs, n
}

type Passage struct {
	Chunks   []int
	Section  string
	PageFrom int
	PageTo   int
	Text     string
	Score    float64
}

type SearchOptions struct {
	K           int              // сколько фрагментов взять до слияния
	TokenBudget int              // 0 — без ограничения
	Count       func(string) int // подсчёт токенов
}

var refRe = regexp.MustCompile(`(?i)(глав[а-яё]*|chapter|част[а-яё]*|part|раздел[а-яё]*|section)\s+(\d{1,3}|[ivxlc]{1,7})\b`)

func family(w string) string {
	w = strings.ToLower(w)
	switch {
	case strings.HasPrefix(w, "глав"), strings.HasPrefix(w, "chapter"):
		return "ch"
	case strings.HasPrefix(w, "част"), strings.HasPrefix(w, "part"):
		return "pt"
	}
	return "sc"
}

func parseNum(s string) int {
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	vals := map[rune]int{'i': 1, 'v': 5, 'x': 10, 'l': 50, 'c': 100}
	total, prev := 0, 0
	rs := []rune(strings.ToLower(s))
	for i := len(rs) - 1; i >= 0; i-- {
		v, ok := vals[rs[i]]
		if !ok {
			return -1
		}
		if v < prev {
			total -= v
		} else {
			total += v
			prev = v
		}
	}
	return total
}

// sectionByReference ищет раздел, на который явно ссылается вопрос.
func (r *Retriever) sectionByReference(q string) (int, bool) {
	m := refRe.FindStringSubmatch(q)
	if m == nil {
		return 0, false
	}
	fam, num := family(m[1]), parseNum(m[2])
	if num < 0 {
		return 0, false
	}
	for _, s := range r.Tree.Sections {
		tm := refRe.FindStringSubmatch(s.Title)
		if tm != nil && family(tm[1]) == fam && parseNum(tm[2]) == num {
			return s.ID, true
		}
		if fam == "ch" { // «3. Название» / «3 Название»
			f := strings.Fields(s.Title)
			if len(f) > 0 && strings.TrimRight(f[0], ".") == strconv.Itoa(num) {
				return s.ID, true
			}
		}
	}
	return 0, false
}

// Search возвращает релевантные фрагменты в порядке документа.
func (r *Retriever) Search(ctx context.Context, query string, o SearchOptions) ([]Passage, error) {
	if o.K <= 0 {
		o.K = 8
	}
	var allow func(int) bool
	if sid, ok := r.sectionByReference(query); ok {
		in := make([]bool, len(r.Tree.Chunks))
		any := false
		for _, sub := range r.subtree[sid] {
			s := r.Tree.Sections[sub]
			for c := s.ChunkFrom; c < s.ChunkTo; c++ {
				in[c], any = true, true
			}
		}
		if any {
			allow = func(i int) bool { return in[i] }
		}
	}
	const pool = 40
	lex := r.bm25.Search(query, pool, allow)

	var dense []vec.Hit
	var secRank map[int]int
	if r.VectorCount() > 0 && r.Emb != nil {
		qv, err := r.Emb.EmbedQuery(ctx, query)
		if err != nil {
			return nil, err
		}
		r.mu.Lock()
		if len(qv) == r.vecs.Dims() {
			dense = r.vecs.Search(qv, pool, allow)
			r.computeCentroids()
			type sc struct {
				id int
				s  float32
			}
			var ss []sc
			for i, c := range r.centroids {
				if c != nil && r.Tree.Sections[i].ChunkTo > r.Tree.Sections[i].ChunkFrom {
					ss = append(ss, sc{i, dotf(qv, c)})
				}
			}
			sort.Slice(ss, func(a, b int) bool { return ss[a].s > ss[b].s })
			secRank = map[int]int{}
			for i := 0; i < len(ss) && i < 3; i++ {
				secRank[ss[i].id] = i
			}
		}
		r.mu.Unlock()
	}

	score := map[int]float64{}
	for rank, h := range lex {
		score[h.Doc] += 1.0 / float64(60+rank)
	}
	for rank, h := range dense {
		score[h.ID] += 1.0 / float64(60+rank)
	}
	if secRank != nil {
		for id := range score {
			if rk, ok := secRank[r.Tree.Chunks[id].Section]; ok {
				score[id] += 0.5 / float64(60+rk)
			}
		}
	}
	type kv struct {
		id int
		s  float64
	}
	var ranked []kv
	for id, s := range score {
		ranked = append(ranked, kv{id, s})
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].s == ranked[j].s {
			return ranked[i].id < ranked[j].id
		}
		return ranked[i].s > ranked[j].s
	})
	// набираем по рангу до K фрагментов и до лимита токенов
	var chosen []kv
	used := 0
	for _, c := range ranked {
		if len(chosen) >= o.K {
			break
		}
		if o.TokenBudget > 0 && o.Count != nil {
			t := o.Count(r.Tree.Chunks[c.id].Text) + 12
			if used+t > o.TokenBudget {
				if len(chosen) == 0 { // хотя бы один фрагмент берём всегда
					chosen = append(chosen, c)
				}
				continue
			}
			used += t
		}
		chosen = append(chosen, c)
	}
	sort.Slice(chosen, func(i, j int) bool { return chosen[i].id < chosen[j].id })
	var out []Passage
	for _, c := range chosen {
		ch := r.Tree.Chunks[c.id]
		sec := r.Tree.Sections[ch.Section].Path
		if n := len(out); n > 0 && out[n-1].Chunks[len(out[n-1].Chunks)-1] == c.id-1 && out[n-1].Section == sec {
			p := &out[n-1]
			p.Text += "\n" + trimOverlap(p.Text, ch.Text)
			p.Chunks = append(p.Chunks, c.id)
			p.PageTo = ch.PageTo
			p.Score += c.s
			continue
		}
		out = append(out, Passage{Chunks: []int{c.id}, Section: sec, PageFrom: ch.PageFrom, PageTo: ch.PageTo, Text: ch.Text, Score: c.s})
	}
	return out, nil
}

// trimOverlap убирает из начала next часть, совпадающую с концом prev (перекрытие чанков).
func trimOverlap(prev, next string) string {
	pr, nr := []rune(prev), []rune(next)
	maxL := min(400, min(len(pr), len(nr)))
	for l := maxL; l >= 20; l-- {
		if string(pr[len(pr)-l:]) == string(nr[:l]) {
			return strings.TrimLeft(string(nr[l:]), "\n ")
		}
	}
	return next
}

func dotf(a, b []float32) float32 {
	var s float32
	for i := range a {
		s += a[i] * b[i]
	}
	return s
}
