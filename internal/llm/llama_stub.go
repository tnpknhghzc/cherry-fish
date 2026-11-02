//go:build nollama

package llm

import "errors"

type Config struct {
	Path      string
	CtxSize   int
	Threads   int
	GPULayers int
}

func NewLlama(c Config) (LLM, error) {
	return nil, errors.New("сборка без поддержки llama.cpp (тег nollama)")
}
