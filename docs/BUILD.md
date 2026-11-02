# Сборка

## Требования разработчика
Go ≥ 1.25, git, cmake, C/C++ компилятор (gcc/clang). Windows — MSYS2 MINGW64.
Конечному пользователю не нужно ничего.

## Windows (из обычного PowerShell)
```
powershell -ExecutionPolicy Bypass -File scripts\build-windows.ps1
```
Скрипт ставит MSYS2 и всё нужное сам и собирает `bin\locallm.exe`. Обычный `make` из PowerShell
не подойдёт: `make deps` требует bash (оболочка MSYS2 MINGW64).

## Шаги (Linux/macOS/MSYS2)
```
make deps     # клонирует llama-go (закреплённый коммит), патчит линковку, собирает llama.cpp
make build    # bin/locallm
```
`build-llama.sh` собирает CPU-код **без `-march=native`** (AVX2/FMA/F16C/BMI2 вручную) — иначе
бинарник, собранный на одной машине, падал бы на другой. Нужен CPU с AVX2 (≈ 2013+).
OpenMP выключен (иначе внешняя зависимость `libgomp`), `libstdc++` линкуется статически.
Проверка: `ldd bin/locallm` должен показывать только `libc`, `libm` (и ld-linux).

`go.mod` содержит `replace github.com/tcpipuk/llama-go => ./third_party/llama-go`: библиотеке нужны
собранные рядом статические `.a`, поэтому через обычный `go get` она не линкуется.
(`go mod tidy` не используйте — он пытается скачать тестовые зависимости llama-go.)

## Релиз «один файл»
```
make models                       # скачать GGUF (нужен интернет, только сборщику)
make bundle LLM=models/qwen2.5-3b-instruct-q4_k_m.gguf EMBED=models/Qwen3-Embedding-0.6B-Q8_0.gguf
```
Результат `bin/locallm-bundle` — единственный файл для распространения. Лимит GitHub Releases — 2 ГБ
на файл, поэтому CI публикует бандл с 1.5B-моделью, а большие модели лежат рядом как `models/*.gguf`.
Ограничение: подпись/нотаризация macOS делается на готовый бандл.

## GPU (необязательно)
`BUILD_TYPE=vulkan ./scripts/build-llama.sh` и запуск с `LOCALLM_GPU_LAYERS=99`. Требует Vulkan-загрузчик на машине
пользователя, поэтому в стандартный релиз не входит. На macOS Metal включён всегда.

## Что проверено
Linux x86-64: сборка, все тесты, прогон на настоящем llama.cpp (крошечные модели со случайными весами),
индексация PDF на 1500 страниц, упаковка/распаковка бандла, HTTP-API.
**Не проверялось:** Windows и macOS (настроены в CI, ожидайте мелких правок в первом прогоне),
качество ответов на настоящих моделях Qwen и реальная скорость на вашем ноутбуке.
