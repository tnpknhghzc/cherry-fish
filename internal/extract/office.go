package extract

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"io"
	"strconv"
	"strings"
)

func newXML(r io.Reader) *xml.Decoder {
	d := xml.NewDecoder(r)
	d.Strict = false
	d.CharsetReader = charsetReader
	return d
}

func attr(se xml.StartElement, local string) string {
	for _, a := range se.Attr {
		if a.Name.Local == local {
			return a.Value
		}
	}
	return ""
}

// ---------- DOCX ----------

func loadDOCX(path string) (*Document, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	data, err := readZipFile(zr, "word/document.xml")
	if err != nil {
		return nil, err
	}
	d := newXML(bytes.NewReader(data))
	var blocks []block
	var cur strings.Builder
	level, inT, inP := 0, false, false
	for {
		tok, err := d.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if !strings.Contains(t.Name.Space, "wordprocessingml") {
				continue
			}
			switch t.Name.Local {
			case "p":
				inP, level = true, 0
				cur.Reset()
			case "pStyle":
				if lv := styleLevel(attr(t, "val")); lv > 0 && level == 0 {
					level = lv
				}
			case "outlineLvl":
				if n, err := strconv.Atoi(attr(t, "val")); err == nil && n < 9 && level == 0 {
					level = n + 1
				}
			case "t":
				inT = true
			case "tab", "br", "cr":
				if inP {
					cur.WriteByte(' ')
				}
			}
		case xml.CharData:
			if inT && inP {
				cur.Write(t)
			}
		case xml.EndElement:
			if !strings.Contains(t.Name.Space, "wordprocessingml") {
				continue
			}
			switch t.Name.Local {
			case "t":
				inT = false
			case "p":
				if s := strings.TrimSpace(cur.String()); s != "" {
					if level > 0 && len([]rune(s)) > 200 {
						level = 0 // «заголовок»-абзац слишком длинный — это текст
					}
					blocks = append(blocks, block{s, level})
				}
				inP = false
			}
		}
	}
	doc := &Document{Format: "docx"}
	if core, err := readZipFile(zr, "docProps/core.xml"); err == nil {
		doc.Title, doc.Author = dcMeta(core)
	}
	doc.Pages, doc.Headings = paginate(blocks)
	return doc, nil
}

func styleLevel(v string) int {
	l := strings.ToLower(v)
	for _, p := range []string{"heading", "заголовок", "title", "название"} {
		if strings.HasPrefix(l, p) {
			if p == "title" || p == "название" {
				return 1
			}
			n, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(l, p)))
			if err == nil && n >= 1 && n <= 9 {
				return n
			}
			return 0
		}
	}
	return 0
}

func dcMeta(data []byte) (title, author string) {
	d := newXML(bytes.NewReader(data))
	var cur string
	for {
		tok, err := d.Token()
		if err != nil {
			return
		}
		switch t := tok.(type) {
		case xml.StartElement:
			cur = t.Name.Local
		case xml.CharData:
			s := strings.TrimSpace(string(t))
			if s == "" {
				continue
			}
			switch cur {
			case "title":
				if title == "" {
					title = s
				}
			case "creator", "initial-creator":
				if author == "" {
					author = s
				}
			}
		case xml.EndElement:
			cur = ""
		}
	}
}

// ---------- ODT ----------

func loadODT(path string) (*Document, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	data, err := readZipFile(zr, "content.xml")
	if err != nil {
		return nil, err
	}
	d := newXML(bytes.NewReader(data))
	var blocks []block
	var cur strings.Builder
	level, inBlock, skip := 0, false, 0
	for {
		tok, err := d.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "note", "tracked-changes", "sequence-decls", "table-of-content-source":
				skip++
			case "h":
				if skip == 0 {
					inBlock, level = true, 1
					if n, err := strconv.Atoi(attr(t, "outline-level")); err == nil && n > 0 {
						level = n
					}
					cur.Reset()
				}
			case "p":
				if skip == 0 && !inBlock {
					inBlock, level = true, 0
					cur.Reset()
				}
			case "s":
				if inBlock {
					n, _ := strconv.Atoi(attr(t, "c"))
					cur.WriteString(strings.Repeat(" ", max(n, 1)))
				}
			case "tab", "line-break":
				if inBlock {
					cur.WriteByte(' ')
				}
			}
		case xml.CharData:
			if inBlock && skip == 0 {
				cur.Write(t)
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "note", "tracked-changes", "sequence-decls", "table-of-content-source":
				skip--
			case "h", "p":
				if inBlock && skip == 0 {
					if s := strings.TrimSpace(cur.String()); s != "" {
						if level > 0 && len([]rune(s)) > 200 {
							level = 0
						}
						blocks = append(blocks, block{s, level})
					}
					inBlock = false
				}
			}
		}
	}
	doc := &Document{Format: "odt"}
	if meta, err := readZipFile(zr, "meta.xml"); err == nil {
		doc.Title, doc.Author = dcMeta(meta)
	}
	doc.Pages, doc.Headings = paginate(blocks)
	return doc, nil
}
