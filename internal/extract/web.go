package extract

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"strings"
)

// htmlBlocks превращает (X)HTML в блоки: заголовки h1–h6 и абзацы.
func htmlBlocks(r io.Reader) (blocks []block, title string) {
	d := xml.NewDecoder(r)
	d.Strict = false
	d.AutoClose = xml.HTMLAutoClose
	d.Entity = xml.HTMLEntity
	d.CharsetReader = charsetReader
	var cur strings.Builder
	level, skip, inTitle := 0, 0, false
	flush := func() {
		if s := collapse(cur.String()); s != "" {
			if level > 0 && len([]rune(s)) > 200 {
				blocks = append(blocks, block{s, 0})
			} else {
				blocks = append(blocks, block{s, level})
			}
		}
		cur.Reset()
		level = 0
	}
	for {
		tok, err := d.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			n := strings.ToLower(t.Name.Local)
			switch n {
			case "script", "style", "head", "svg", "nav":
				if n == "head" {
					continue
				}
				skip++
			case "title":
				inTitle = true
			case "h1", "h2", "h3", "h4", "h5", "h6":
				flush()
				level = int(n[1] - '0')
			case "p", "div", "li", "tr", "blockquote", "section", "article", "pre", "ul", "ol", "table", "br":
				flush()
			}
		case xml.CharData:
			if inTitle {
				title += string(t)
			} else if skip == 0 {
				cur.Write(t)
			}
		case xml.EndElement:
			n := strings.ToLower(t.Name.Local)
			switch n {
			case "script", "style", "svg", "nav":
				skip--
			case "title":
				inTitle = false
			case "h1", "h2", "h3", "h4", "h5", "h6", "p", "div", "li", "tr", "blockquote", "section", "article", "pre", "table":
				flush()
			}
		}
	}
	flush()
	return blocks, collapse(title)
}

func loadHTML(p string) (*Document, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	blocks, title := htmlBlocks(strings.NewReader(decodeText(b)))
	doc := &Document{Format: "html", Title: title}
	doc.Pages, doc.Headings = paginate(blocks)
	return doc, nil
}

// ---------- EPUB ----------

func loadEPUB(p string) (*Document, error) {
	zr, err := zip.OpenReader(p)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	cont, err := readZipFile(zr, "META-INF/container.xml")
	if err != nil {
		return nil, fmt.Errorf("это не EPUB: %w", err)
	}
	opfPath := ""
	d := newXML(bytes.NewReader(cont))
	for {
		tok, err := d.Token()
		if err != nil {
			break
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "rootfile" {
			opfPath = attr(se, "full-path")
			break
		}
	}
	if opfPath == "" {
		return nil, fmt.Errorf("EPUB: не найден OPF")
	}
	opf, err := readZipFile(zr, opfPath)
	if err != nil {
		return nil, err
	}
	doc := &Document{Format: "epub"}
	manifest := map[string]string{}
	var spine []string
	d = newXML(bytes.NewReader(opf))
	var cur string
	for {
		tok, err := d.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			cur = t.Name.Local
			switch cur {
			case "item":
				manifest[attr(t, "id")] = attr(t, "href")
			case "itemref":
				spine = append(spine, attr(t, "idref"))
			}
		case xml.CharData:
			s := strings.TrimSpace(string(t))
			if s == "" {
				continue
			}
			if cur == "title" && doc.Title == "" {
				doc.Title = s
			} else if cur == "creator" && doc.Author == "" {
				doc.Author = s
			}
		case xml.EndElement:
			cur = ""
		}
	}
	base := path.Dir(opfPath)
	var blocks []block
	for _, id := range spine {
		href, ok := manifest[id]
		if !ok {
			continue
		}
		if u, err := url.PathUnescape(href); err == nil {
			href = u
		}
		if i := strings.IndexByte(href, '#'); i >= 0 {
			href = href[:i]
		}
		data, err := readZipFile(zr, path.Clean(path.Join(base, href)))
		if err != nil {
			continue
		}
		bs, _ := htmlBlocks(bytes.NewReader(data))
		blocks = append(blocks, bs...)
	}
	doc.Pages, doc.Headings = paginate(blocks)
	return doc, nil
}

// ---------- FB2 ----------

func loadFB2Zip(p string) (*Document, error) {
	zr, err := zip.OpenReader(p)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	for _, f := range zr.File {
		if strings.HasSuffix(strings.ToLower(f.Name), ".fb2") {
			data, err := readZipFile(zr, f.Name)
			if err != nil {
				return nil, err
			}
			return parseFB2(data)
		}
	}
	return nil, fmt.Errorf("в архиве нет .fb2 файла")
}

func loadFB2File(p string) (*Document, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	return parseFB2(b)
}

func parseFB2(data []byte) (*Document, error) {
	d := newXML(bytes.NewReader(data))
	doc := &Document{Format: "fb2"}
	var blocks []block
	var stack []string
	secDepth, skipBody, skipBin := 0, false, false
	var cur strings.Builder
	var first, last string
	inTitle := false
	var titleParts []string
	flushP := func() {
		if s := collapse(cur.String()); s != "" {
			if inTitle {
				titleParts = append(titleParts, s)
			} else {
				blocks = append(blocks, block{s, 0})
			}
		}
		cur.Reset()
	}
	for {
		tok, err := d.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			n := t.Name.Local
			stack = append(stack, n)
			switch n {
			case "binary":
				skipBin = true
			case "body":
				skipBody = attr(t, "name") == "notes"
			case "section":
				secDepth++
			case "title":
				inTitle = true
				titleParts = nil
			case "empty-line":
				flushP()
			}
		case xml.CharData:
			if skipBin || skipBody {
				continue
			}
			in := ""
			if len(stack) > 0 {
				in = stack[len(stack)-1]
			}
			switch in {
			case "book-title":
				if doc.Title == "" {
					doc.Title = strings.TrimSpace(string(t))
				}
			case "first-name":
				if first == "" && inAuthor(stack) {
					first = strings.TrimSpace(string(t))
				}
			case "last-name":
				if last == "" && inAuthor(stack) {
					last = strings.TrimSpace(string(t))
				}
			default:
				if inBody(stack) {
					cur.Write(t)
				}
			}
		case xml.EndElement:
			n := t.Name.Local
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
			switch n {
			case "binary":
				skipBin = false
			case "body":
				skipBody = false
			case "p", "v", "subtitle", "text-author":
				if !skipBody {
					flushP()
				}
			case "title":
				flushP()
				if !skipBody && len(titleParts) > 0 {
					lv := secDepth
					if lv < 1 {
						lv = 1
					}
					blocks = append(blocks, block{strings.Join(titleParts, " "), lv})
				}
				inTitle = false
				titleParts = nil
			case "section":
				secDepth--
			}
		}
	}
	doc.Author = strings.TrimSpace(first + " " + last)
	doc.Pages, doc.Headings = paginate(blocks)
	return doc, nil
}

func inAuthor(stack []string) bool {
	for _, s := range stack {
		if s == "author" {
			return true
		}
	}
	return false
}

func inBody(stack []string) bool {
	for _, s := range stack {
		if s == "body" {
			return true
		}
	}
	return false
}
