module github.com/tnpknhghzc/locallm

go 1.25

require (
	github.com/gen2brain/go-fitz v1.28.2
	github.com/tcpipuk/llama-go v0.0.0-20261016142444-4e0097fcc270
)

// llama-go собирается вместе с llama.cpp (статические библиотеки), поэтому
// берётся из third_party/ — его готовит `make deps` (см. Makefile).
replace github.com/tcpipuk/llama-go => ./third_party/llama-go
