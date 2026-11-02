// Package payload позволяет «приклеить» файлы (модели GGUF) к концу исполняемого
// файла и достать их при первом запуске. Так пользователь получает ОДИН файл
// (программа + модели), а Go-линкер не упирается в лимиты на встраиваемые данные.
//
// Формат хвоста: [файлы...][JSON-оглавление][uint64 длина оглавления]["LLMPAYL1"]
package payload

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const magic = "LLMPAYL1"

type Entry struct {
	Name   string `json:"name"`
	Offset int64  `json:"offset"`
	Size   int64  `json:"size"`
}

type Manifest struct {
	Files []Entry `json:"files"`
	ID    string  `json:"id"` // хэш оглавления: меняется, если состав/размеры изменились
}

// Read возвращает оглавление и размер «чистого» исполняемого файла (без payload).
// Если payload нет — (nil, size, nil).
func Read(exe string) (*Manifest, int64, error) {
	f, err := os.Open(exe)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, 0, err
	}
	size := st.Size()
	if size < 16 {
		return nil, size, nil
	}
	tail := make([]byte, 16)
	if _, err := f.ReadAt(tail, size-16); err != nil {
		return nil, size, err
	}
	if string(tail[8:]) != magic {
		return nil, size, nil
	}
	tocLen := int64(binary.LittleEndian.Uint64(tail[:8]))
	if tocLen <= 0 || tocLen > size-16 {
		return nil, size, errors.New("payload: повреждённое оглавление")
	}
	buf := make([]byte, tocLen)
	if _, err := f.ReadAt(buf, size-16-tocLen); err != nil {
		return nil, size, err
	}
	var m Manifest
	if err := json.Unmarshal(buf, &m); err != nil {
		return nil, size, err
	}
	base := size - 16 - tocLen
	for _, e := range m.Files {
		base = min(base, e.Offset)
	}
	return &m, base, nil
}

// Extract распаковывает файлы payload в dir. Уже распакованные файлы (по метке
// payload.id) пропускаются, поэтому повторный запуск мгновенный.
func Extract(exe, dir string, progress func(name string, done, total int64)) ([]string, error) {
	m, _, err := Read(exe)
	if err != nil || m == nil {
		return nil, err
	}
	marker := filepath.Join(dir, ".payload-id")
	var names []string
	for _, e := range m.Files {
		names = append(names, filepath.Join(dir, e.Name))
	}
	if b, err := os.ReadFile(marker); err == nil && string(b) == m.ID {
		ok := true
		for _, e := range m.Files {
			if st, err := os.Stat(filepath.Join(dir, e.Name)); err != nil || st.Size() != e.Size {
				ok = false
			}
		}
		if ok {
			return names, nil
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	src, err := os.Open(exe)
	if err != nil {
		return nil, err
	}
	defer src.Close()
	for _, e := range m.Files {
		name := filepath.Base(e.Name) // защита от path traversal
		dst := filepath.Join(dir, name)
		tmp := dst + ".part"
		out, err := os.Create(tmp)
		if err != nil {
			return nil, err
		}
		_, err = io.Copy(out, io.NewSectionReader(src, e.Offset, e.Size))
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			os.Remove(tmp)
			return nil, err
		}
		if err := os.Rename(tmp, dst); err != nil {
			return nil, err
		}
		if progress != nil {
			progress(name, e.Size, e.Size)
		}
	}
	return names, os.WriteFile(marker, []byte(m.ID), 0o644)
}

// Pack копирует exe (отбрасывая старый payload) в out и дописывает файлы.
func Pack(exe, out string, files []string) error {
	_, base, err := Read(exe)
	if err != nil {
		return err
	}
	src, err := os.Open(exe)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.OpenFile(out, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(dst, io.NewSectionReader(src, 0, base)); err != nil {
		dst.Close()
		return err
	}
	pos := base
	m := Manifest{}
	for _, p := range files {
		in, err := os.Open(p)
		if err != nil {
			dst.Close()
			return err
		}
		n, err := io.Copy(dst, in)
		in.Close()
		if err != nil {
			dst.Close()
			return err
		}
		m.Files = append(m.Files, Entry{Name: filepath.Base(p), Offset: pos, Size: n})
		pos += n
	}
	h := sha256.New()
	for _, e := range m.Files {
		fmt.Fprintf(h, "%s:%d;", e.Name, e.Size)
	}
	m.ID = hex.EncodeToString(h.Sum(nil))[:16]
	toc, _ := json.Marshal(m)
	tail := make([]byte, 8)
	binary.LittleEndian.PutUint64(tail, uint64(len(toc)))
	for _, b := range [][]byte{toc, tail, []byte(magic)} {
		if _, err := dst.Write(b); err != nil {
			dst.Close()
			return err
		}
	}
	return dst.Close()
}
