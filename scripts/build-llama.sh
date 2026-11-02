#!/usr/bin/env bash
# Готовит llama.cpp для статической линковки в Go-бинарник:
#   1) клонирует tcpipuk/llama-go на проверенный коммит (вместе с llama.cpp),
#   2) патчит линковку (без libgomp, libstdc++ статически),
#   3) собирает статические библиотеки с ПЕРЕНОСИМЫМИ флагами CPU (AVX2, без -march=native),
#      чтобы бинарник, собранный в CI, запускался на любом ноутбуке 2013+ года.
# Запускать из корня репозитория:  ./scripts/build-llama.sh
# Windows: из оболочки MSYS2 MINGW64 (pacman -S mingw-w64-x86_64-{gcc,cmake,make} git).
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DEST="$ROOT/third_party/llama-go"
REPO="https://github.com/tcpipuk/llama-go"
REF="${LLAMA_GO_REF:-4e0097fcc270b6e282c29a2a15d00f3cf00a29a8}"

MARK="$(uname -s)-${MSYSTEM:-none}-$REF"
if [ -f "$DEST/libbinding.a" ] && [ "$(cat "$DEST/.built-with" 2>/dev/null)" = "$MARK" ] && [ -z "${FORCE:-}" ]; then
  echo "llama.cpp уже собран ($DEST). FORCE=1 — пересобрать."; exit 0
fi

mkdir -p "$ROOT/third_party"
if [ ! -d "$DEST/.git" ]; then git clone "$REPO" "$DEST"; fi
cd "$DEST"
git fetch --quiet origin "$REF" 2>/dev/null || git fetch --quiet origin
git checkout --quiet -f "$REF"
git submodule update --init --depth 1 --recursive

# --- патчи (идемпотентны) ---
# go.mod: у зависимости убираем тестовые зависимости и «go 1.27.2» → собирается любым Go ≥ 1.25
printf 'module github.com/tcpipuk/llama-go\n\ngo 1.25\n' > go.mod
rm -f go.sum
# линковка: без OpenMP (libgomp — внешняя зависимость), libstdc++ — статически (кроме macOS)
sed -i.bak 's/ -lgomp//' linkage_static.go
case "$(uname -s)" in
  Darwin) ;;
  *) sed -i.bak '/^#cgo !darwin LDFLAGS/ s/-lstdc++ -lm/-l:libstdc++.a -lm/' linkage_static.go ;;
esac
rm -f linkage_static.go.bak

# --- флаги сборки ---
ARCH="$(uname -m)"
CM="-DCMAKE_BUILD_TYPE=Release -DGGML_OPENMP=OFF -DLLAMA_CURL=OFF"
case "$ARCH" in
  x86_64|amd64|AMD64) CM="$CM -DGGML_NATIVE=OFF -DGGML_AVX=ON -DGGML_AVX2=ON -DGGML_FMA=ON -DGGML_F16C=ON -DGGML_BMI2=ON" ;;
esac
export BUILD_TYPE="${BUILD_TYPE:-}"
if [ "$(uname -s)" = "Darwin" ]; then
  export BUILD_TYPE=metal                   # Metal встроен в систему — доп. установки не нужны
  CM="$CM -DGGML_METAL_EMBED_LIBRARY=ON"    # шейдеры внутри библиотеки, а не отдельным файлом
fi
# GPU через Vulkan (необязательно, см. docs/BUILD.md): BUILD_TYPE=vulkan ./scripts/build-llama.sh
export CMAKE_ARGS="$CM ${EXTRA_CMAKE_ARGS:-}"
export CMAKE_BUILD_PARALLEL_LEVEL="${JOBS:-$( (nproc 2>/dev/null || sysctl -n hw.ncpu 2>/dev/null || echo 2) )}"

# Windows (MSYS2): CMake по умолчанию берёт Ninja и создаёт *.obj, а Makefile llama-go ждёт *.o
# (ggml.c.o). Принудительно используем «Unix Makefiles» и сбрасываем старый кэш сборки.
case "$(uname -s)" in
  MINGW*|MSYS*)
    export CMAKE_GENERATOR="Unix Makefiles"; rm -rf "$DEST/build"
    # на Windows компилятор создаёт *.obj, а Makefile llama-go копирует *.o → правим пути
    sed -i.bak 's#\(CMakeFiles/[A-Za-z0-9_./-]*\)\.o\([ "]\)#\1.obj\2#g' Makefile; rm -f Makefile.bak
    # без OpenSSL (иначе в бинарник тянется libcrypto.dll — нарушает «один файл»)
    export CMAKE_ARGS="$CMAKE_ARGS -DLLAMA_OPENSSL=OFF -DCMAKE_C_FLAGS=-D_WIN32_WINNT=0x0A00 -DCMAKE_CXX_FLAGS=-D_WIN32_WINNT=0x0A00"
    # cpp-httplib использует CreateFile2, которого нет в заголовках MinGW → заменяем на CreateFileW
    perl -0pi -e 's/::CreateFile2\(wpath\.c_str\(\), GENERIC_READ,\s*FILE_SHARE_READ \| FILE_SHARE_WRITE, OPEN_EXISTING, NULL\)/::CreateFileW(wpath.c_str(), GENERIC_READ, FILE_SHARE_READ | FILE_SHARE_WRITE, NULL, OPEN_EXISTING, FILE_ATTRIBUTE_NORMAL, NULL)/' llama.cpp/vendor/cpp-httplib/httplib.cpp ;;
esac

case "$(uname -s)" in
  MINGW*|MSYS*)
    # На Windows статические библиотеки ggml называются ggml.a (без префикса lib), и копирующий
    # шаг Makefile llama-go падает. libbinding.a к этому моменту уже собран — копируем сами.
    make libbinding.a || true
    [ -f libbinding.a ] || { echo "ОШИБКА: libbinding.a не собран"; exit 1; }
    cp build/common/libllama-common.a build/common/libllama-common-base.a build/src/libllama.a .
    for n in ggml ggml-base ggml-cpu; do
      src="build/ggml/src/lib$n.a"; [ -f "$src" ] || src="build/ggml/src/$n.a"
      cp "$src" "lib$n.a"
    done ;;
  *) make libbinding.a ;;
esac
echo "$MARK" > "$DEST/.built-with"
echo "Готово: $DEST"
