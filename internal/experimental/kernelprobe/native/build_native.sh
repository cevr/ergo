#!/usr/bin/env bash
set -eu

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SCRATCH="${KERNELPROBE_SCRATCH:-/tmp/kernelprobe}"
OUT_DIR="${KERNELPROBE_LIB_DIR:-$SCRATCH/lib}"
PSYCHOSCRIPT_DIR="${PSYCHOSCRIPT_DIR:-$SCRATCH/psychoscript}"
LUAU_DIR="${LUAU_DIR:-$SCRATCH/luau}"
RUSTUP_HOME="${RUSTUP_HOME:-$SCRATCH/rustup}"
CARGO_HOME="${CARGO_HOME:-$SCRATCH/cargo}"

export RUSTUP_HOME CARGO_HOME
export PATH="$CARGO_HOME/bin:$PATH"

mkdir -p "$SCRATCH" "$OUT_DIR"

if [ ! -d "$PSYCHOSCRIPT_DIR/rust" ]; then
  git clone --depth 1 https://github.com/anomalyco/psychoscript "$PSYCHOSCRIPT_DIR"
fi

if [ ! -d "$LUAU_DIR/VM" ]; then
  git clone --depth 1 https://github.com/luau-lang/luau "$LUAU_DIR"
fi

echo "==> Building psychoscript_c (cdylib + staticlib)..."
PSY_BUILD="$SCRATCH/psychoscript_c_build"
mkdir -p "$PSY_BUILD/src"
cp "$SCRIPT_DIR/psychoscript_c/src/lib.rs" "$PSY_BUILD/src/lib.rs"
sed "s|/tmp/kernelprobe/psychoscript|$PSYCHOSCRIPT_DIR|g" "$SCRIPT_DIR/psychoscript_c/Cargo.toml" > "$PSY_BUILD/Cargo.toml"
cp "$PSYCHOSCRIPT_DIR/rust/Cargo.lock" "$PSY_BUILD/Cargo.lock"

RUSTFLAGS="${RUSTFLAGS:--C embed-bitcode=no}" CARGO_BUILD_JOBS="${CARGO_BUILD_JOBS:-4}" cargo build \
  --manifest-path "$PSY_BUILD/Cargo.toml" \
  --release \
  -j "${CARGO_BUILD_JOBS:-4}"

cp "$PSY_BUILD/target/release/libpsychoscript_c.so" "$OUT_DIR/libpsychoscript_c.so"
cp "$PSY_BUILD/target/release/libpsychoscript_c.a" "$OUT_DIR/libpsychoscript_c.a"

echo "==> Building luau_c shared & static libraries (Luau VM + Compiler, -j4)..."
LUAU_PIC="$SCRATCH/luau_pic"
mkdir -p "$LUAU_PIC"

g++ -O2 -DNDEBUG -fPIC -std=c++17 \
  -I"$SCRIPT_DIR/include" \
  -I"$LUAU_DIR/VM/include" \
  -I"$LUAU_DIR/Compiler/include" \
  -I"$LUAU_DIR/Common/include" \
  -c "$SCRIPT_DIR/luau_c/luau_c.cpp" \
  -o "$OUT_DIR/luau_c.o"

compile_luau_src() {
  f="$1"
  obj="$LUAU_PIC/$(basename "$(dirname "$(dirname "$f")")")_$(basename "$f").o"
  if [ ! -f "$obj" ] || [ "$f" -nt "$obj" ]; then
    case "$f" in
      */VM/src/*)
        g++ -O2 -DNDEBUG -fno-math-errno -fPIC -std=c++11 \
          -I"$LUAU_DIR/Common/include" -I"$LUAU_DIR/VM/include" \
          -c "$f" -o "$obj"
        ;;
      *)
        g++ -O2 -DNDEBUG -fno-math-errno -fPIC -std=c++17 \
          -I"$LUAU_DIR/Common/include" -I"$LUAU_DIR/Ast/include" \
          -I"$LUAU_DIR/Bytecode/include" -I"$LUAU_DIR/Compiler/include" \
          -c "$f" -o "$obj"
        ;;
    esac
  fi
}
export -f compile_luau_src
export LUAU_DIR LUAU_PIC

printf '%s\n' \
  "$LUAU_DIR"/Common/src/*.cpp \
  "$LUAU_DIR"/Ast/src/*.cpp \
  "$LUAU_DIR"/Bytecode/src/*.cpp \
  "$LUAU_DIR"/Compiler/src/*.cpp \
  "$LUAU_DIR"/VM/src/*.cpp \
  | xargs -P 4 -I {} bash -c 'compile_luau_src "$@"' _ {}

ar rcs "$OUT_DIR/libluau_c.a" "$OUT_DIR/luau_c.o" "$LUAU_PIC"/*.o
g++ -shared -O2 -o "$OUT_DIR/libluau_c.so" "$OUT_DIR/luau_c.o" "$LUAU_PIC"/*.o -lm -lpthread

ls -lh "$OUT_DIR"
