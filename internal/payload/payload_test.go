package payload

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPackExtract(t *testing.T) {
	d := t.TempDir()
	exe := filepath.Join(d, "exe")
	os.WriteFile(exe, []byte("FAKE-EXECUTABLE"), 0o755)
	m1 := filepath.Join(d, "a.gguf")
	m2 := filepath.Join(d, "b.gguf")
	os.WriteFile(m1, []byte("model-one"), 0o644)
	os.WriteFile(m2, []byte("model-two-longer"), 0o644)
	out := filepath.Join(d, "bundle")
	if err := Pack(exe, out, []string{m1, m2}); err != nil {
		t.Fatal(err)
	}
	// повторная упаковка бандла не должна накапливать payload
	out2 := filepath.Join(d, "bundle2")
	if err := Pack(out, out2, []string{m1}); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(out2); st.Size() >= mustSize(t, out) {
		t.Fatal("старый payload не отброшен")
	}
	dir := filepath.Join(d, "models")
	names, err := Extract(out, dir, nil)
	if err != nil || len(names) != 2 {
		t.Fatalf("%v %v", names, err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "b.gguf"))
	if string(b) != "model-two-longer" {
		t.Fatalf("содержимое: %q", b)
	}
	if _, err := Extract(out, dir, nil); err != nil { // второй запуск — быстрый путь
		t.Fatal(err)
	}
	m, base, _ := Read(exe)
	if m != nil || base != int64(len("FAKE-EXECUTABLE")) {
		t.Fatal("чистый exe распознан как bundle")
	}
}

func mustSize(t *testing.T, p string) int64 {
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return st.Size()
}
