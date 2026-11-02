package vec

import (
	"path/filepath"
	"testing"
)

func TestSearchAndSave(t *testing.T) {
	x := New(3)
	vs := [][]float32{{1, 0, 0}, {0, 1, 0}, {0.9, 0.1, 0}, {0, 0, 1}}
	for _, v := range vs {
		Normalize(v)
	}
	x.Add(vs...)
	h := x.Search([]float32{1, 0, 0}, 2, nil)
	if len(h) != 2 || h[0].ID != 0 || h[1].ID != 2 {
		t.Fatalf("%+v", h)
	}
	p := filepath.Join(t.TempDir(), "v.bin")
	if err := x.Save(p); err != nil {
		t.Fatal(err)
	}
	y, err := Load(p)
	if err != nil || y.Len() != 4 || y.Dims() != 3 {
		t.Fatal(err, y)
	}
	if h := y.Search([]float32{0, 0, 1}, 1, nil); h[0].ID != 3 {
		t.Fatalf("%+v", h)
	}
	if err := x.Add([]float32{1}); err == nil {
		t.Fatal("ожидалась ошибка размерности")
	}
}
