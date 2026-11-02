package textproc

import (
	"math"
	"sort"
)

type posting struct {
	doc uint32
	tf  uint16
}

// BM25 — компактный инвертированный индекс. Строится из текста за доли секунды
// даже для книги на 1500 страниц, поэтому на диск не сохраняется.
type BM25 struct {
	inv    map[string][]posting
	lens   []uint16
	avgLen float64
	k1, b  float64
}

func NewBM25() *BM25 { return &BM25{inv: map[string][]posting{}, k1: 1.2, b: 0.75} }

// Add добавляет документ; его номер = порядок добавления.
func (x *BM25) Add(text string) {
	toks := Tokens(text)
	id := uint32(len(x.lens))
	tf := map[string]uint16{}
	for _, t := range toks {
		if tf[t] < 65535 {
			tf[t]++
		}
	}
	for t, c := range tf {
		x.inv[t] = append(x.inv[t], posting{id, c})
	}
	n := len(toks)
	if n > 65535 {
		n = 65535
	}
	x.lens = append(x.lens, uint16(n))
}

// Finish вычисляет среднюю длину; вызывать после последнего Add.
func (x *BM25) Finish() {
	var s float64
	for _, l := range x.lens {
		s += float64(l)
	}
	if len(x.lens) > 0 {
		x.avgLen = s / float64(len(x.lens))
	}
}

type Hit struct {
	Doc   int
	Score float64
}

// Search возвращает top-k документов. allow (если не nil) ограничивает множество.
func (x *BM25) Search(query string, k int, allow func(doc int) bool) []Hit {
	qt := Tokens(query)
	if len(qt) == 0 || len(x.lens) == 0 {
		return nil
	}
	N := float64(len(x.lens))
	scores := map[uint32]float64{}
	seen := map[string]bool{}
	for _, t := range qt {
		if seen[t] {
			continue
		}
		seen[t] = true
		pl := x.inv[t]
		if len(pl) == 0 {
			continue
		}
		df := float64(len(pl))
		idf := math.Log(1 + (N-df+0.5)/(df+0.5))
		for _, p := range pl {
			if allow != nil && !allow(int(p.doc)) {
				continue
			}
			tf := float64(p.tf)
			dl := float64(x.lens[p.doc])
			scores[p.doc] += idf * tf * (x.k1 + 1) / (tf + x.k1*(1-x.b+x.b*dl/math.Max(x.avgLen, 1)))
		}
	}
	hits := make([]Hit, 0, len(scores))
	for d, s := range scores {
		hits = append(hits, Hit{int(d), s})
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Score == hits[j].Score {
			return hits[i].Doc < hits[j].Doc
		}
		return hits[i].Score > hits[j].Score
	})
	if k > 0 && len(hits) > k {
		hits = hits[:k]
	}
	return hits
}
