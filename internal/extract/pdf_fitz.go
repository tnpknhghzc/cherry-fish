//go:build !nopdf

package extract

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/gen2brain/go-fitz"
)

func loadPDF(ctx context.Context, path string, prog Progress) (*Document, error) {
	d, err := fitz.New(path)
	if err != nil {
		return nil, fmt.Errorf("не удалось открыть PDF: %w", err)
	}
	defer d.Close()
	n := d.NumPage()
	raw := make([]string, n)
	for i := 0; i < n; i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		t, err := d.Text(i)
		if err != nil {
			t = "" // одна битая страница не должна убивать всю книгу
		}
		raw[i] = t
		if i%10 == 0 {
			prog(i, n)
		}
	}
	doc := &Document{Format: "pdf"}
	md := d.Metadata()
	doc.Title, doc.Author = strings.TrimSpace(md["title"]), strings.TrimSpace(md["author"])
	doc.Pages = cleanPDFPages(raw)
	if toc, err := d.ToC(); err == nil {
		for _, o := range toc {
			t := collapse(o.Title)
			pg := outlinePage(o)
			if t == "" || pg < 1 || pg > n {
				continue
			}
			lv := o.Level
			if lv < 1 {
				lv = 1
			}
			doc.Headings = append(doc.Headings, Heading{Level: lv, Title: t, Page: pg})
		}
	}
	if len(doc.Headings) == 0 {
		doc.Headings = detectHeadings(doc.Pages)
	}
	return doc, nil
}

var uriPageRe = regexp.MustCompile(`page=(\d+)`)

// outlinePage возвращает 1-based номер страницы закладки. У go-fitz поле Page —
// 0-based, а в URI вида "#page=3" номер 1-based; URI надёжнее, берём его
// (проверено на реальном PDF: иначе все главы сдвигаются на страницу).
func outlinePage(o fitz.Outline) int {
	if m := uriPageRe.FindStringSubmatch(o.URI); m != nil {
		var v int
		fmt.Sscanf(m[1], "%d", &v)
		return v
	}
	return o.Page + 1
}
