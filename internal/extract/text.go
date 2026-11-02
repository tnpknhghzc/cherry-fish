package extract

import (
	"os"
	"regexp"
	"strings"
	"unicode/utf8"
)

var mdHead = regexp.MustCompile(`^(#{1,6})\s+(.+)$`)

func loadText(p string) (*Document, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	s := strings.ReplaceAll(decodeText(b), "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	paras := strings.Split(s, "\n\n")
	if len(paras) < 3 { // файл без пустых строк: абзац = строка
		paras = strings.Split(s, "\n")
	}
	var blocks []block
	for _, para := range paras {
		para = strings.TrimSpace(para)
		if para == "" {
			continue
		}
		first := para
		rest := ""
		if i := strings.IndexByte(para, '\n'); i >= 0 {
			first, rest = strings.TrimSpace(para[:i]), strings.TrimSpace(para[i+1:])
		}
		if m := mdHead.FindStringSubmatch(first); m != nil {
			blocks = append(blocks, block{m[2], len(m[1])})
			if rest != "" {
				blocks = append(blocks, block{collapse(rest), 0})
			}
			continue
		}
		if utf8.RuneCountInString(first) <= 90 && (chapRe.MatchString(first) || wordRe.MatchString(first)) {
			lv := 1
			if low := strings.ToLower(first); strings.HasPrefix(low, "глава") || strings.HasPrefix(low, "chapter") {
				lv = 2
			}
			blocks = append(blocks, block{first, lv})
			if rest != "" {
				blocks = append(blocks, block{collapse(rest), 0})
			}
			continue
		}
		blocks = append(blocks, block{collapse(para), 0})
	}
	doc := &Document{Format: "txt"}
	doc.Pages, doc.Headings = paginate(blocks)
	return doc, nil
}

// ---------- RTF ----------

func loadRTF(p string) (*Document, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	src := string(b)
	cp1251 := strings.Contains(src, `\ansicpg1251`)
	skipDest := map[string]bool{"fonttbl": true, "colortbl": true, "stylesheet": true, "info": true, "pict": true, "header": true, "footer": true, "footnote": true}
	var sb strings.Builder
	type grp struct{ skip bool }
	stack := []grp{{false}}
	skip := func() bool { return stack[len(stack)-1].skip }
	i, n := 0, len(src)
	for i < n {
		c := src[i]
		switch c {
		case '{':
			stack = append(stack, grp{skip()})
			i++
		case '}':
			if len(stack) > 1 {
				stack = stack[:len(stack)-1]
			}
			i++
		case '\\':
			i++
			if i >= n {
				break
			}
			c2 := src[i]
			switch {
			case c2 == '\'' && i+2 < n:
				var v int
				for _, h := range src[i+1 : i+3] {
					v <<= 4
					switch {
					case h >= '0' && h <= '9':
						v |= int(h - '0')
					case h >= 'a' && h <= 'f':
						v |= int(h-'a') + 10
					case h >= 'A' && h <= 'F':
						v |= int(h-'A') + 10
					}
				}
				if !skip() {
					if cp1251 {
						sb.WriteString(cp1251ToUTF8([]byte{byte(v)}))
					} else {
						sb.WriteRune(rune(v))
					}
				}
				i += 3
			case c2 == '*':
				stack[len(stack)-1].skip = true
				i++
			case c2 == '\\' || c2 == '{' || c2 == '}':
				if !skip() {
					sb.WriteByte(c2)
				}
				i++
			case c2 == '~':
				sb.WriteByte(' ')
				i++
			default:
				j := i
				for j < n && ((src[j] >= 'a' && src[j] <= 'z') || (src[j] >= 'A' && src[j] <= 'Z')) {
					j++
				}
				word := src[i:j]
				k := j
				if k < n && (src[k] == '-' || (src[k] >= '0' && src[k] <= '9')) {
					k++
					for k < n && src[k] >= '0' && src[k] <= '9' {
						k++
					}
				}
				param := src[j:k]
				if k < n && src[k] == ' ' {
					k++
				}
				switch {
				case skipDest[word]:
					stack[len(stack)-1].skip = true
				case word == "par" || word == "line" || word == "sect":
					if !skip() {
						sb.WriteString("\n\n")
					}
				case word == "tab":
					sb.WriteByte(' ')
				case word == "u" && param != "":
					v := 0
					neg := strings.HasPrefix(param, "-")
					for _, d := range strings.TrimPrefix(param, "-") {
						v = v*10 + int(d-'0')
					}
					if neg {
						v = 65536 - v
					}
					if !skip() {
						sb.WriteRune(rune(v))
					}
					if k < n && src[k] != '\\' { // пропускаем символ-заменитель
						k++
					}
				}
				i = k
			}
		case '\n', '\r':
			i++
		default:
			if !skip() {
				sb.WriteByte(c)
			}
			i++
		}
	}
	var blocks []block
	for _, para := range strings.Split(sb.String(), "\n\n") {
		if s := collapse(para); s != "" {
			blocks = append(blocks, block{s, 0})
		}
	}
	doc := &Document{Format: "rtf"}
	doc.Pages, doc.Headings = paginate(blocks)
	return doc, nil
}
