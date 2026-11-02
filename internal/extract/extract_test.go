package extract

import (
	"archive/zip"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeZip(t *testing.T, path string, files map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for n, c := range files {
		w, _ := zw.Create(n)
		w.Write([]byte(c))
	}
	zw.Close()
	f.Close()
}

func long(s string) string { return strings.Repeat(s+" ", 40) }

func TestDOCX(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.docx")
	body := `<?xml version="1.0"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>
<w:p><w:pPr><w:pStyle w:val="Heading1"/></w:pPr><w:r><w:t>Глава первая</w:t></w:r></w:p>
<w:p><w:r><w:t>` + long("Это текст про керлинг и камни.") + `</w:t></w:r></w:p>
<w:p><w:pPr><w:pStyle w:val="Heading2"/></w:pPr><w:r><w:t>Раздел 1.1</w:t></w:r></w:p>
<w:p><w:r><w:t>` + long("Второй абзац.") + `</w:t></w:r></w:p></w:body></w:document>`
	writeZip(t, p, map[string]string{"word/document.xml": body})
	d, err := Load(context.Background(), p, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Headings) != 2 || d.Headings[0].Title != "Глава первая" || d.Headings[1].Level != 2 {
		t.Fatalf("заголовки: %+v", d.Headings)
	}
}

func TestODT(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.odt")
	body := `<?xml version="1.0"?><office:document-content xmlns:office="urn:oasis:names:tc:opendocument:xmlns:office:1.0" xmlns:text="urn:oasis:names:tc:opendocument:xmlns:text:1.0"><office:body><office:text>
<text:h text:outline-level="1">Введение</text:h>
<text:p>` + long("Привет, <text:span>мир</text:span>.") + `</text:p>
<text:p>Сноска<text:note><text:note-body><text:p>НЕ ДОЛЖНО ПОПАСТЬ</text:p></text:note-body></text:note> после.</text:p>
</office:text></office:body></office:document-content>`
	writeZip(t, p, map[string]string{"content.xml": body})
	d, err := Load(context.Background(), p, nil)
	if err != nil {
		t.Fatal(err)
	}
	all := strings.Join(d.Pages, "\n")
	if strings.Contains(all, "НЕ ДОЛЖНО") {
		t.Fatal("текст сноски попал в документ")
	}
	if len(d.Headings) != 1 || d.Headings[0].Title != "Введение" {
		t.Fatalf("заголовки: %+v", d.Headings)
	}
}

func TestFB2cp1251(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.fb2")
	xmlUTF := `<?xml version="1.0" encoding="windows-1251"?><FictionBook><description><title-info><author><first-name>Лев</first-name><last-name>Толстой</last-name></author><book-title>Война</book-title></title-info></description><body><section><title><p>Часть первая</p></title><p>` + long("Текст главы") + `</p></section></body><binary id="x">AAAA</binary></FictionBook>`
	// кодируем в cp1251
	var b []byte
	for _, r := range xmlUTF {
		switch {
		case r < 0x80:
			b = append(b, byte(r))
		case r >= 0x0410 && r <= 0x044F:
			b = append(b, byte(0xC0+(r-0x0410)))
		case r == 0x0401:
			b = append(b, 0xA8)
		case r == 0x0451:
			b = append(b, 0xB8)
		}
	}
	os.WriteFile(p, b, 0o644)
	d, err := Load(context.Background(), p, nil)
	if err != nil {
		t.Fatal(err)
	}
	if d.Title != "Война" || d.Author != "Лев Толстой" {
		t.Fatalf("метаданные: %q / %q", d.Title, d.Author)
	}
	if len(d.Headings) != 1 || d.Headings[0].Title != "Часть первая" {
		t.Fatalf("заголовки: %+v", d.Headings)
	}
}

func TestTextCP1251AndMarkdown(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.md")
	os.WriteFile(p, []byte("# Заголовок\n\n"+long("Абзац текста.")+"\n\n## Подзаголовок\n\n"+long("Ещё.")), 0o644)
	d, err := Load(context.Background(), p, nil)
	if err != nil || len(d.Headings) != 2 || d.Headings[1].Level != 2 {
		t.Fatalf("%v %+v", err, d)
	}
}

func TestNoText(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.txt")
	os.WriteFile(p, []byte("x"), 0o644)
	if _, err := Load(context.Background(), p, nil); err != ErrNoText {
		t.Fatalf("ожидалась ErrNoText, получено %v", err)
	}
}

func TestCleanPDFPages(t *testing.T) {
	var raw []string
	for i := 1; i <= 12; i++ {
		raw = append(raw, "Заголовок книги\nПервая строка абзаца с достаточно длинным текстом для проверки\nпе-\nренос слова в строке и продолжение текста тут\nвторая строка абзаца заканчивается.\nКонец страницы без номера но с текстом\n"+string(rune('0'+i%10))+"\n")
	}
	out := cleanPDFPages(raw)
	if strings.Contains(out[3], "Заголовок книги") {
		t.Fatalf("колонтитул не удалён: %q", out[3])
	}
	if !strings.Contains(out[3], "перенос слова") {
		t.Fatalf("перенос не склеен: %q", out[3])
	}
}

func TestCleanMeta(t *testing.T) {
	if got := cleanMeta("\xfe\xff\x04\x1f\x04\x40\x04\x38"); got != "При" {
		t.Fatalf("UTF-16 BOM: %q", got)
	}
	if got := cleanMeta("\x00\x00\x00\x00"); got != "" {
		t.Fatalf("нули: %q", got)
	}
	if got := cleanMeta("  Война и мир \x01"); got != "Война и мир" {
		t.Fatalf("управляющие: %q", got)
	}
}
