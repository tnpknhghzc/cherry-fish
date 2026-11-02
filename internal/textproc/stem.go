// Package textproc содержит токенизацию, стемминг (русский/английский) и BM25.
package textproc

import (
	"strings"
	"unicode"
)

const ruVowels = "аеиоуыэюя"

func isVowel(r rune) bool { return strings.ContainsRune(ruVowels, r) }

// Русские окончания (Snowball-подобный алгоритм, упрощённый).
// Списки упорядочены от длинных окончаний к коротким.
var (
	pgGroup1 = []string{"вшись", "вши", "в"}
	pgGroup2 = []string{"ившись", "ывшись", "ивши", "ывши", "ив", "ыв"}
	reflexiv = []string{"ся", "сь"}
	adjectiv = []string{"ими", "ыми", "его", "ого", "ему", "ому", "ее", "ие", "ые", "ое", "ей", "ий", "ый", "ой", "ем", "им", "ым", "ом", "их", "ых", "ую", "юю", "ая", "яя", "ою", "ею"}
	partG1   = []string{"ем", "нн", "вш", "ющ", "щ"}
	partG2   = []string{"ивш", "ывш", "ующ"}
	verbG1   = []string{"ешь", "нно", "ете", "йте", "ла", "на", "ли", "ло", "но", "ет", "ют", "ны", "ть", "ем", "й", "л", "н"}
	verbG2   = []string{"ейте", "уйте", "ила", "ыла", "ена", "ите", "или", "ыли", "ило", "ыло", "ено", "ует", "уют", "ены", "ить", "ыть", "ишь", "ей", "уй", "ил", "ыл", "им", "ым", "ен", "ят", "ит", "ыт", "ую", "ю"}
	nounEnd  = []string{"иями", "ями", "ами", "ией", "иям", "ием", "иях", "ев", "ов", "ие", "ье", "еи", "ии", "ей", "ой", "ий", "ям", "ем", "ам", "ом", "ах", "ях", "ию", "ью", "ия", "ья", "а", "е", "и", "й", "о", "у", "ы", "ь", "ю", "я"}
	superl   = []string{"ейше", "ейш"}
)

func hasSuffixIn(w []rune, rvStart int, list []string, needA bool) ([]rune, bool) {
	for _, s := range list {
		sr := []rune(s)
		if len(w) < len(sr) || len(w)-len(sr) < rvStart {
			continue
		}
		if !equalRunes(w[len(w)-len(sr):], sr) {
			continue
		}
		if needA {
			p := len(w) - len(sr) - 1
			if p < rvStart || (w[p] != 'а' && w[p] != 'я') {
				continue
			}
		}
		return w[:len(w)-len(sr)], true
	}
	return w, false
}

func equalRunes(a, b []rune) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// StemRU приводит русское слово (нижний регистр, ё→е) к основе.
func StemRU(word string) string {
	w := []rune(word)
	if len(w) <= 3 {
		return word
	}
	rv := len(w)
	for i, r := range w {
		if isVowel(r) {
			rv = i + 1
			break
		}
	}
	r1 := regionAfter(w, 0)
	r2 := regionAfter(w, r1)

	done := false
	if t, ok := hasSuffixIn(w, rv, pgGroup1, true); ok {
		w, done = t, true
	} else if t, ok := hasSuffixIn(w, rv, pgGroup2, false); ok {
		w, done = t, true
	}
	if !done {
		if t, ok := hasSuffixIn(w, rv, reflexiv, false); ok {
			w = t
		}
		if t, ok := hasSuffixIn(w, rv, adjectiv, false); ok {
			w = t
			if t2, ok := hasSuffixIn(w, rv, partG1, true); ok {
				w = t2
			} else if t2, ok := hasSuffixIn(w, rv, partG2, false); ok {
				w = t2
			}
		} else if t, ok := hasSuffixIn(w, rv, verbG1, true); ok {
			w = t
		} else if t, ok := hasSuffixIn(w, rv, verbG2, false); ok {
			w = t
		} else if t, ok := hasSuffixIn(w, rv, nounEnd, false); ok {
			w = t
		}
	}
	if t, ok := hasSuffixIn(w, rv, []string{"и"}, false); ok {
		w = t
	}
	if t, ok := hasSuffixIn(w, r2, []string{"ость", "ост"}, false); ok {
		w = t
	}
	if t, ok := hasSuffixIn(w, rv, superl, false); ok {
		w = t
	}
	if n := len(w); n >= 2 && w[n-1] == 'н' && w[n-2] == 'н' {
		w = w[:n-1]
	} else if t, ok := hasSuffixIn(w, rv, []string{"ь"}, false); ok {
		w = t
	}
	if len(w) < 2 {
		return word
	}
	return string(w)
}

func regionAfter(w []rune, from int) int {
	for i := from + 1; i < len(w); i++ {
		if !isVowel(w[i]) && isVowel(w[i-1]) {
			return i + 1
		}
	}
	return len(w)
}

// StemEN — лёгкий английский стеммер (достаточно для BM25).
func StemEN(w string) string {
	if len(w) <= 3 {
		return w
	}
	rules := [][2]string{
		{"ational", "ate"}, {"ization", "ize"}, {"ications", "ic"}, {"ication", "ic"},
		{"nesses", ""}, {"ness", ""}, {"ments", ""}, {"ment", ""}, {"ingly", ""}, {"edly", ""},
		{"sses", "ss"}, {"ies", "y"}, {"ing", ""}, {"ed", ""}, {"ly", ""}, {"es", ""}, {"s", ""},
	}
	for _, r := range rules {
		if strings.HasSuffix(w, r[0]) && len(w)-len(r[0]) >= 3 {
			if r[0] == "s" && strings.HasSuffix(w, "ss") {
				return w
			}
			return w[:len(w)-len(r[0])] + r[1]
		}
	}
	return w
}

func isCyrillic(r rune) bool { return unicode.Is(unicode.Cyrillic, r) }
