// Package extract читает документы разных форматов и приводит их к единой
// модели: список «страниц» текста + оглавление (заголовки со ссылками на страницы).
// Для форматов без страниц (docx, odt, epub, fb2, txt...) страницы виртуальные
// (~2800 символов), чтобы ссылки «стр. N» в ответах оставались осмысленными.
package extract

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
)

type Heading struct {
	Level int // 1 = верхний уровень
	Title string
	Page  int // 1-based
}

type Document struct {
	Title    string
	Author   string
	Format   string
	Pages    []string
	Headings []Heading
}

// Progress вызывается во время чтения (done из total).
type Progress func(done, total int)

var ErrNoText = errors.New("в документе не найден текст: похоже, это скан без текстового слоя (OCR не поддерживается)")

// Supported — список поддерживаемых расширений.
var Supported = []string{".pdf", ".docx", ".odt", ".epub", ".fb2", ".fb2.zip", ".txt", ".md", ".markdown", ".html", ".htm", ".rtf"}

// Load определяет формат и извлекает документ.
func Load(ctx context.Context, path string, prog Progress) (*Document, error) {
	if prog == nil {
		prog = func(int, int) {}
	}
	lower := strings.ToLower(path)
	var doc *Document
	var err error
	switch {
	case strings.HasSuffix(lower, ".pdf"):
		doc, err = loadPDF(ctx, path, prog)
	case strings.HasSuffix(lower, ".docx"):
		doc, err = loadDOCX(path)
	case strings.HasSuffix(lower, ".odt"):
		doc, err = loadODT(path)
	case strings.HasSuffix(lower, ".epub"):
		doc, err = loadEPUB(path)
	case strings.HasSuffix(lower, ".fb2.zip"), strings.HasSuffix(lower, ".zip"):
		doc, err = loadFB2Zip(path)
	case strings.HasSuffix(lower, ".fb2"):
		doc, err = loadFB2File(path)
	case strings.HasSuffix(lower, ".rtf"):
		doc, err = loadRTF(path)
	case strings.HasSuffix(lower, ".html"), strings.HasSuffix(lower, ".htm"):
		doc, err = loadHTML(path)
	case strings.HasSuffix(lower, ".txt"), strings.HasSuffix(lower, ".md"), strings.HasSuffix(lower, ".markdown"):
		doc, err = loadText(path)
	default:
		doc, err = sniff(ctx, path, prog)
	}
	if err != nil {
		return nil, err
	}
	doc.Title, doc.Author = cleanMeta(doc.Title), cleanMeta(doc.Author)
	if doc.Title == "" {
		doc.Title = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	total := 0
	for _, p := range doc.Pages {
		total += utf8.RuneCountInString(p)
	}
	if len(doc.Pages) == 0 || total < 30 {
		return nil, ErrNoText
	}
	if doc.Format == "pdf" && total/len(doc.Pages) < 25 && len(doc.Pages) > 3 {
		return nil, ErrNoText
	}
	prog(len(doc.Pages), len(doc.Pages))
	return doc, nil
}

func sniff(ctx context.Context, path string, prog Progress) (*Document, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	head := make([]byte, 8)
	n, _ := f.Read(head)
	f.Close()
	head = head[:n]
	switch {
	case bytes.HasPrefix(head, []byte("%PDF")):
		return loadPDF(ctx, path, prog)
	case bytes.HasPrefix(head, []byte("{\\rtf")):
		return loadRTF(path)
	case bytes.HasPrefix(head, []byte("PK")):
		zr, err := zip.OpenReader(path)
		if err != nil {
			return nil, err
		}
		defer zr.Close()
		for _, f := range zr.File {
			switch f.Name {
			case "word/document.xml":
				return loadDOCX(path)
			case "content.xml":
				return loadODT(path)
			case "META-INF/container.xml":
				return loadEPUB(path)
			}
		}
		return loadFB2Zip(path)
	}
	return loadText(path)
}

// ---------- общие блоки и пагинация ----------

type block struct {
	text  string
	level int // 0 — обычный абзац; >0 — заголовок
}

const pageChars = 2800

// paginate превращает блоки в виртуальные страницы и список заголовков.
func paginate(blocks []block) ([]string, []Heading) {
	var pages []string
	var heads []Heading
	var cur strings.Builder
	curLen := 0
	flush := func() {
		if cur.Len() > 0 {
			pages = append(pages, strings.TrimSpace(cur.String()))
			cur.Reset()
			curLen = 0
		}
	}
	for _, b := range blocks {
		t := strings.TrimSpace(b.text)
		if t == "" {
			continue
		}
		l := utf8.RuneCountInString(t)
		if b.level > 0 {
			if curLen > pageChars*2/3 {
				flush()
			}
			heads = append(heads, Heading{Level: b.level, Title: collapse(t), Page: len(pages) + 1})
		} else if curLen > 0 && curLen+l > pageChars {
			flush()
		}
		cur.WriteString(t)
		cur.WriteString("\n\n")
		curLen += l + 2
	}
	flush()
	return pages, heads
}

var wsRe = regexp.MustCompile(`\s+`)

func collapse(s string) string { return strings.TrimSpace(wsRe.ReplaceAllString(s, " ")) }

// ---------- кодировки ----------

var cp1251hi = [64]rune{
	0x0402, 0x0403, 0x201A, 0x0453, 0x201E, 0x2026, 0x2020, 0x2021, 0x20AC, 0x2030, 0x0409, 0x2039, 0x040A, 0x040C, 0x040B, 0x040F,
	0x0452, 0x2018, 0x2019, 0x201C, 0x201D, 0x2022, 0x2013, 0x2014, 0x0098, 0x2122, 0x0459, 0x203A, 0x045A, 0x045C, 0x045B, 0x045F,
	0x00A0, 0x040E, 0x045E, 0x0408, 0x00A4, 0x0490, 0x00A6, 0x00A7, 0x0401, 0x00A9, 0x0404, 0x00AB, 0x00AC, 0x00AD, 0x00AE, 0x0407,
	0x00B0, 0x00B1, 0x0406, 0x0456, 0x0491, 0x00B5, 0x00B6, 0x00B7, 0x0451, 0x2116, 0x0454, 0x00BB, 0x0458, 0x0405, 0x0455, 0x0457,
}

func cp1251ToUTF8(b []byte) string {
	var sb strings.Builder
	sb.Grow(len(b) * 2)
	for _, c := range b {
		switch {
		case c < 0x80:
			sb.WriteByte(c)
		case c < 0xC0:
			sb.WriteRune(cp1251hi[c-0x80])
		default:
			sb.WriteRune(rune(0x0410 + int(c-0xC0)))
		}
	}
	return sb.String()
}

// decodeText: BOM → UTF-8/UTF-16; валидный UTF-8 как есть; иначе windows-1251.
func decodeText(b []byte) string {
	switch {
	case bytes.HasPrefix(b, []byte{0xEF, 0xBB, 0xBF}):
		return string(b[3:])
	case bytes.HasPrefix(b, []byte{0xFF, 0xFE}), bytes.HasPrefix(b, []byte{0xFE, 0xFF}):
		le := b[0] == 0xFF
		var sb strings.Builder
		for i := 2; i+1 < len(b); i += 2 {
			var u uint16
			if le {
				u = uint16(b[i]) | uint16(b[i+1])<<8
			} else {
				u = uint16(b[i])<<8 | uint16(b[i+1])
			}
			sb.WriteRune(rune(u))
		}
		return sb.String()
	case utf8.Valid(b):
		return string(b)
	}
	return cp1251ToUTF8(b)
}

type cp1251Reader struct{ r io.Reader }

func (c cp1251Reader) Read(p []byte) (int, error) {
	buf := make([]byte, len(p)/2)
	if len(buf) == 0 {
		return 0, io.ErrShortBuffer
	}
	n, err := c.r.Read(buf)
	if n > 0 {
		s := cp1251ToUTF8(buf[:n])
		copy(p, s)
		return len(s), err
	}
	return 0, err
}

// charsetReader — для encoding/xml (fb2 часто в windows-1251).
func charsetReader(label string, in io.Reader) (io.Reader, error) {
	switch strings.ToLower(strings.TrimSpace(label)) {
	case "windows-1251", "cp1251", "win-1251", "1251":
		return cp1251Reader{in}, nil
	}
	return in, nil
}

func readZipFile(zr *zip.ReadCloser, name string) ([]byte, error) {
	for _, f := range zr.File {
		if f.Name == name {
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			defer rc.Close()
			return io.ReadAll(io.LimitReader(rc, 512<<20))
		}
	}
	return nil, fmt.Errorf("в архиве нет файла %s", name)
}

// cleanMeta приводит метаданные (название/автор) к нормальному тексту: PDF часто хранит их
// в UTF-16 (с BOM или без) или с управляющими символами. Непригодное значение → "".
func cleanMeta(s string) string {
	b := []byte(s)
	if len(b) >= 2 && ((b[0] == 0xFE && b[1] == 0xFF) || (b[0] == 0xFF && b[1] == 0xFE)) || bytes.IndexByte(b, 0) >= 0 {
		le := len(b) >= 2 && b[0] == 0xFF && b[1] == 0xFE
		if len(b) >= 2 && (b[0] == 0xFE && b[1] == 0xFF || le) {
			b = b[2:]
		}
		var sb strings.Builder
		for i := 0; i+1 < len(b); i += 2 {
			var u uint16
			if le {
				u = uint16(b[i]) | uint16(b[i+1])<<8
			} else {
				u = uint16(b[i])<<8 | uint16(b[i+1])
			}
			sb.WriteRune(rune(u))
		}
		s = sb.String()
	}
	if !utf8.ValidString(s) {
		return ""
	}
	var out strings.Builder
	for _, r := range s {
		if r >= 0x20 && r != 0x7f && r != 0xFFFD {
			out.WriteRune(r)
		}
	}
	return strings.TrimSpace(out.String())
}
