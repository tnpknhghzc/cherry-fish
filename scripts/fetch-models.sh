#!/usr/bin/env bash
# ТОЛЬКО для разработчика/сборщика релиза (нужен интернет). Конечному пользователю
# запускать это НЕ нужно: модели вшиваются в бинарник командой `make bundle`.
#
#   ./scripts/fetch-models.sh [папка]      # по умолчанию ./models
#
# URL-ы указывают на официальные репозитории Qwen на Hugging Face. Перед релизом
# проверьте, что имена файлов актуальны, и при желании добавьте SHA-256 в EXPECT.
set -euo pipefail
OUT="${1:-models}"; mkdir -p "$OUT"
HF="https://huggingface.co"

get() { # url файл
  if [ -f "$OUT/$2" ]; then echo "есть: $2"; return; fi
  echo "скачиваю: $2"
  curl -L --fail --retry 3 -C - -o "$OUT/$2.part" "$1" && mv "$OUT/$2.part" "$OUT/$2"
}

# Языковые модели (достаточно одной; по умолчанию — 3B, 1.5B — для слабых машин)
get "$HF/Qwen/Qwen2.5-3B-Instruct-GGUF/resolve/main/qwen2.5-3b-instruct-q4_k_m.gguf"   qwen2.5-3b-instruct-q4_k_m.gguf
[ -n "${WITH_1_5B:-}" ] && get "$HF/Qwen/Qwen2.5-1.5B-Instruct-GGUF/resolve/main/qwen2.5-1.5b-instruct-q4_k_m.gguf" qwen2.5-1.5b-instruct-q4_k_m.gguf
[ -n "${WITH_7B:-}" ]   && get "$HF/Qwen/Qwen2.5-7B-Instruct-GGUF/resolve/main/qwen2.5-7b-instruct-q4_k_m.gguf"     qwen2.5-7b-instruct-q4_k_m.gguf

# Модель эмбеддингов (мультиязычная, хорошо понимает русский)
get "$HF/Qwen/Qwen3-Embedding-0.6B-GGUF/resolve/main/Qwen3-Embedding-0.6B-Q8_0.gguf"   Qwen3-Embedding-0.6B-Q8_0.gguf

echo "Готово: $OUT"
echo "Более быстрая альтернатива для индексации: multilingual-e5-small в GGUF (см. docs/MODELS.md)."
