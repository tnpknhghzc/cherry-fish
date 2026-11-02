// Package vec — плоский (brute-force) векторный индекс. Для книги на 1500
// страниц это ~5–10 тыс. векторов: поиск занимает единицы миллисекунд,
// поэтому ANN/HNSW и SQLite здесь не нужны.
package vec

import (
	"container/heap"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"os"
)

type Index struct {
	dims int
	data []float32 // N*dims, векторы L2-нормализованы
}

func New(dims int) *Index { return &Index{dims: dims} }

func (x *Index) Dims() int { return x.dims }
func (x *Index) Len() int {
	if x.dims == 0 {
		return 0
	}
	return len(x.data) / x.dims
}

// Normalize нормализует вектор на месте.
func Normalize(v []float32) {
	var s float64
	for _, f := range v {
		s += float64(f) * float64(f)
	}
	if s == 0 {
		return
	}
	inv := float32(1 / math.Sqrt(s))
	for i := range v {
		v[i] *= inv
	}
}

// Add добавляет уже нормализованные векторы.
func (x *Index) Add(vs ...[]float32) error {
	for _, v := range vs {
		if len(v) != x.dims {
			return errors.New("vec: неверная размерность вектора")
		}
		x.data = append(x.data, v...)
	}
	return nil
}

type Hit struct {
	ID    int
	Score float32
}

type minHeap []Hit

func (h minHeap) Len() int            { return len(h) }
func (h minHeap) Less(i, j int) bool  { return h[i].Score < h[j].Score }
func (h minHeap) Swap(i, j int)       { h[i], h[j] = h[j], h[i] }
func (h *minHeap) Push(x interface{}) { *h = append(*h, x.(Hit)) }
func (h *minHeap) Pop() interface{} {
	o := *h
	n := len(o)
	it := o[n-1]
	*h = o[:n-1]
	return it
}

// Search возвращает top-k по косинусному сходству (по убыванию).
func (x *Index) Search(q []float32, k int, allow func(id int) bool) []Hit {
	n := x.Len()
	if n == 0 || len(q) != x.dims || k <= 0 {
		return nil
	}
	h := &minHeap{}
	for i := 0; i < n; i++ {
		if allow != nil && !allow(i) {
			continue
		}
		s := dot(q, x.data[i*x.dims:(i+1)*x.dims])
		if h.Len() < k {
			heap.Push(h, Hit{i, s})
		} else if s > (*h)[0].Score {
			(*h)[0] = Hit{i, s}
			heap.Fix(h, 0)
		}
	}
	out := make([]Hit, h.Len())
	for i := len(out) - 1; i >= 0; i-- {
		out[i] = heap.Pop(h).(Hit)
	}
	return out
}

// Vector возвращает i-й вектор (без копирования).
func (x *Index) Vector(i int) []float32 { return x.data[i*x.dims : (i+1)*x.dims] }

func dot(a, b []float32) float32 {
	var s0, s1, s2, s3 float32
	i := 0
	for ; i+4 <= len(a); i += 4 {
		s0 += a[i] * b[i]
		s1 += a[i+1] * b[i+1]
		s2 += a[i+2] * b[i+2]
		s3 += a[i+3] * b[i+3]
	}
	for ; i < len(a); i++ {
		s0 += a[i] * b[i]
	}
	return s0 + s1 + s2 + s3
}

const magic = "LLV1"

// Save атомарно сохраняет индекс.
func (x *Index) Save(path string) error {
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	hdr := make([]byte, 12)
	copy(hdr, magic)
	binary.LittleEndian.PutUint32(hdr[4:], uint32(x.dims))
	binary.LittleEndian.PutUint32(hdr[8:], uint32(x.Len()))
	if _, err = f.Write(hdr); err == nil {
		buf := make([]byte, 4*len(x.data))
		for i, v := range x.data {
			binary.LittleEndian.PutUint32(buf[4*i:], math.Float32bits(v))
		}
		_, err = f.Write(buf)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// Load читает индекс; при отсутствии файла возвращает ошибку os.ErrNotExist.
func Load(path string) (*Index, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	hdr := make([]byte, 12)
	if _, err := io.ReadFull(f, hdr); err != nil {
		return nil, err
	}
	if string(hdr[:4]) != magic {
		return nil, errors.New("vec: неизвестный формат файла")
	}
	dims := int(binary.LittleEndian.Uint32(hdr[4:]))
	n := int(binary.LittleEndian.Uint32(hdr[8:]))
	buf := make([]byte, 4*dims*n)
	if _, err := io.ReadFull(f, buf); err != nil {
		return nil, err
	}
	x := &Index{dims: dims, data: make([]float32, dims*n)}
	for i := range x.data {
		x.data[i] = math.Float32frombits(binary.LittleEndian.Uint32(buf[4*i:]))
	}
	return x, nil
}
