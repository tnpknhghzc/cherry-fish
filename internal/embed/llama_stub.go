//go:build nollama

package embed

import "errors"

// NewLlama в сборке с тегом nollama недоступен (для тестов без cgo-библиотек).
func NewLlama(path string, threads int, queryPrefix, docPrefix string) (Embedder, error) {
	return nil, errors.New("сборка без поддержки llama.cpp (тег nollama)")
}
