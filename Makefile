# locallm — сборка одним файлом. Типичный путь разработчика:
#   make deps      # один раз: собрать llama.cpp (статические библиотеки)
#   make build     # bin/locallm
#   make test      # быстрые тесты (не требуют llama.cpp)
#   make bundle LLM=models/qwen2.5-3b-instruct-q4_k_m.gguf EMBED=models/Qwen3-Embedding-0.6B-Q8_0.gguf
.PHONY: deps build test test-real bundle models clean help

GO      ?= go
LLAMA   := $(CURDIR)/third_party/llama-go
export LIBRARY_PATH   := $(LLAMA)
export C_INCLUDE_PATH := $(LLAMA)
TAGS    ?=
VERSION ?= $(shell git describe --tags --always 2>/dev/null || echo dev)
ifeq ($(OS),Windows_NT)
  UNAME := Windows
  EXE   := .exe
  EXTLD := -static
else
  UNAME := $(shell uname -s)
  EXE   :=
  EXTLD := -static-libgcc
  ifeq ($(UNAME),Darwin)
    EXTLD :=
  endif
endif
LDFLAGS := -s -w -X main.version=$(VERSION)
ifneq ($(EXTLD),)
  LDFLAGS += -extldflags '$(EXTLD)'
endif

# Windows без MSYS2 (обычный PowerShell/cmd): bash нет, поэтому deps/build делегируются
# скрипту, который сам ставит MSYS2 и запускает make внутри него (там MSYSTEM задан).
ifeq ($(OS)$(MSYSTEM),Windows_NT)
deps build:
	powershell -NoProfile -ExecutionPolicy Bypass -File scripts/build-windows.ps1 -Target build -Tags "$(TAGS)"
else
help:
	@sed -n '1,6p' Makefile

deps:
	./scripts/build-llama.sh

build:
	@test -f $(LLAMA)/libbinding.a || (echo "Сначала: make deps"; exit 1)
	$(GO) build -trimpath -tags "$(TAGS)" -ldflags "$(LDFLAGS)" -o bin/locallm$(EXE) ./cmd/locallm
	@echo "Готово: bin/locallm$(EXE)"

test:
	$(GO) vet -tags nollama ./...
	$(GO) test -tags nollama ./...

# Прогон с НАСТОЯЩИМ llama.cpp на крошечных моделях со случайными весами
test-real:
	pip install --quiet gguf numpy || true
	python3 scripts/make_test_models.py .testmodels
	LOCALLM_TEST_MODELS=$(CURDIR)/.testmodels $(GO) test ./pkg/locallm -run TestRealLlama -count=1 -v

models:
	./scripts/fetch-models.sh models

# Один файл «программа + модели»
bundle: build
	@test -n "$(LLM)" -a -n "$(EMBED)" || (echo "Укажите LLM=... EMBED=..."; exit 1)
	./bin/locallm$(EXE) pack -o bin/locallm-bundle$(EXE) $(LLM) $(EMBED)

clean:
	rm -rf bin .testmodels
endif
