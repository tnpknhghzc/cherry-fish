// Package hw определяет ресурсы машины (RAM, число потоков).
package hw

import (
	"os"
	"runtime"
	"strconv"
)

type Info struct {
	TotalRAM uint64 // байт
	AvailRAM uint64
}

// Threads подбирает число потоков для инференса (≈ физические ядра).
func Threads() int {
	if v, err := strconv.Atoi(os.Getenv("LOCALLM_THREADS")); err == nil && v > 0 {
		return v
	}
	n := runtime.NumCPU()
	if n <= 4 {
		return n
	}
	if p := perfCores(); p > 0 {
		return p
	}
	return n / 2
}
