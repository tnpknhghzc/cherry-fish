package extract

import (
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

var (
	hyphenRe  = regexp.MustCompile(`(\pL)[-\x{00AD}]\n(\pL)`)
	pageNumRe = regexp.MustCompile(`^[\s\-–—.]*\d{1,4}[\s\-–—.]*$`)
	digitsRe  = regexp.MustCompile(`\d+`)
)

// cleanPDFPages: убирает колонтитулы и номера страниц, склеивает переносы
// и строки одного абзаца.
func cleanPDFPages(raw []string) []string {
	n := len(raw)
	lines := make([][]string, n)
	freq := map[string]int{}
	for i, t := range raw {
		t = strings.ReplaceAll(t, "\r", "")
		var ls []string
		for _, l := range strings.Split(t, "\n") {
			ls = append(ls, strings.TrimRight(l, " \t"))
		}
		lines[i] = ls
		seen := map[string]bool{}
		for _, idx := range edgeIdx(ls) {
			k := normEdge(ls[idx])
			if k != "" && !seen[k] {
				seen[k] = true
				freq[k]++
			}
		}
	}
	threshold := n / 4
	if threshold < 3 {
		threshold = 3
	}
	out := make([]string, n)
	for i := range lines {
		ls := lines[i]
		drop := map[int]bool{}
		for _, idx := range edgeIdx(ls) {
			l := strings.TrimSpace(ls[idx])
			k := normEdge(l)
			if pageNumRe.MatchString(l) || (n >= 8 && freq[k] >= threshold && utf8.RuneCountInString(l) < 120) {
				drop[idx] = true
			}
		}
		var kept []string
		for j, l := range ls {
			if !drop[j] {
				kept = append(kept, l)
			}
		}
		out[i] = joinParagraphs(kept)
	}
	// переносы между страницами
	for i := 0; i+1 < n; i++ {
		a, b := out[i], out[i+1]
		if strings.HasSuffix(a, "-") && len(b) > 0 {
			r, _ := utf8.DecodeRuneInString(b)
			if unicode.IsLower(r) {
				// «слово-» + «продолжение» на следующей странице
				cut := strings.LastIndex(a, "-")
				sp := strings.IndexAny(b, " \n")
				if sp < 0 {
					sp = len(b)
				}
				out[i] = a[:cut] + b[:sp]
				out[i+1] = strings.TrimLeft(b[sp:], " ")
			}
		}
	}
	return out
}

func edgeIdx(ls []string) []int {
	var idx []int
	for i, l := range ls {
		if strings.TrimSpace(l) != "" {
			idx = append(idx, i)
			if len(idx) == 2 {
				break
			}
		}
	}
	cnt := 0
	for i := len(ls) - 1; i >= 0 && cnt < 2; i-- {
		if strings.TrimSpace(ls[i]) != "" {
			idx = append(idx, i)
			cnt++
		}
	}
	return idx
}

func normEdge(l string) string {
	l = strings.ToLower(strings.TrimSpace(l))
	return collapse(digitsRe.ReplaceAllString(l, "#"))
}

// joinParagraphs склеивает «жёсткие» переносы строк внутри абзацев.
func joinParagraphs(ls []string) string {
	var lens []int
	for _, l := range ls {
		if l = strings.TrimSpace(l); l != "" {
			lens = append(lens, utf8.RuneCountInString(l))
		}
	}
	if len(lens) == 0 {
		return ""
	}
	sort.Ints(lens)
	maxLen := lens[len(lens)*9/10]
	var sb strings.Builder
	prev := ""
	for _, l := range ls {
		l = strings.TrimSpace(l)
		if l == "" {
			if prev != "" {
				sb.WriteString("\n\n")
			}
			prev = ""
			continue
		}
		if prev != "" {
			r, _ := utf8.DecodeRuneInString(l)
			endsSentence := strings.ContainsRune(".!?…:»”\")", lastRune(prev))
			short := utf8.RuneCountInString(prev) < maxLen*8/10
			if endsSentence && (short || r == '—' || r == '–') {
				sb.WriteString("\n\n")
			} else if strings.HasSuffix(prev, "-") && !strings.HasSuffix(prev, " -") {
				// перенос слова: убираем дефис только если дальше строчная буква
				if unicode.IsLower(r) {
					s := sb.String()
					sb.Reset()
					sb.WriteString(strings.TrimSuffix(s, "-"))
				} else {
					sb.WriteString(" ")
				}
			} else {
				sb.WriteString(" ")
			}
		}
		sb.WriteString(l)
		prev = l
	}
	return strings.TrimSpace(sb.String())
}

func lastRune(s string) rune {
	r, _ := utf8.DecodeLastRuneInString(s)
	return r
}

var (
	chapRe = regexp.MustCompile(`(?i)^(глава|часть|раздел|книга|chapter|part|section|book)\s+([0-9]+|[ivxlcdm]+|[а-яa-z-]{3,15})[\s.:—–-]*.{0,70}$`)
	wordRe = regexp.MustCompile(`(?i)^(пролог|эпилог|предисловие|введение|заключение|послесловие|prologue|epilogue|preface|introduction|conclusion|afterword)\.?$`)
)

// detectHeadings — эвристика, когда у PDF нет закладок: «Глава N», «Chapter N»...
func detectHeadings(pages []string) []Heading {
	var hs []Heading
	for i, p := range pages {
		cnt := 0
		for _, l := range strings.Split(p, "\n") {
			l = collapse(l)
			if l == "" {
				continue
			}
			cnt++
			if cnt > 3 {
				break
			}
			if utf8.RuneCountInString(l) <= 90 && (chapRe.MatchString(l) || wordRe.MatchString(l)) {
				lv := 1
				low := strings.ToLower(l)
				if strings.HasPrefix(low, "глава") || strings.HasPrefix(low, "chapter") {
					lv = 2
				}
				hs = append(hs, Heading{Level: lv, Title: l, Page: i + 1})
				break
			}
		}
	}
	// если «глав» слишком мало, это, скорее всего, ложные срабатывания
	if len(hs) < 3 {
		return nil
	}
	return hs
}
