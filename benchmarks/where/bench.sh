#!/usr/bin/env bash
# spike-where benchmarks: checker time at N constrained call sites,
# Go binary size and stencil counts against SH1's GC-shape sharing, and
# JavaScript output size, for where-clause dispatch (style b) against an
# explicit ordering argument (style a).
#
# Usage: bench.sh EF_PROTO WORKDIR
#   EF_PROTO  the prototype compiler, built from branch wf10/spike-where-proto
#             (`go build -o EF_PROTO ./cmd/ergo` in that worktree)
#   WORKDIR   scratch directory for generated programs and builds (`ergo build`
#             also writes generated Go under WORKDIR/dist)
# Writes a report to stdout. Checker time is `ergo check --target go`'s
# checkMicros and totalMicros, median of 3, with the load average recorded
# before each size.
set -u
here=$(cd "$(dirname "$0")" && pwd)
EF=${1:?usage: bench.sh EF_PROTO WORKDIR}
WORK=${2:?usage: bench.sh EF_PROTO WORKDIR}
mkdir -p "$WORK" && cd "$WORK" || exit 1
median() { sort -n | sed -n 2p; }
micros() { grep -o "\"$1\":[0-9]*" | cut -d: -f2; }

echo "== environment"
echo "date $(date -u +%Y-%m-%dT%H:%M:%SZ)"
echo "host $(uname -srm), $(nproc) cpus"
echo "go on PATH: $(go version)"
echo "ergo $(sha256sum "$EF" | cut -c1-16)"

echo
echo "== checker time (types=16, distinct layouts; microseconds; median of 3)"
printf '%-6s %-5s %10s %10s %10s %10s  %s\n' N style check total parse bytes load
for n in 1000 2000 4000 8000; do
  load=$(cut -d' ' -f1-3 /proc/loadavg)
  declare -A checks=() totals=() parses=()
  for style in a b; do
    node "$here/gen.mjs" --style "$style" --sites "$n" --types 16 > "check-$style-$n.ef"
  done
  # Interleave the styles so load drift falls on both alike.
  for run in 1 2 3; do
    for style in a b; do
      json=$("$EF" check "check-$style-$n.ef" --target go)
      echo "$json" | grep -q '"checked":true' || { echo "check failed: $style $n"; exit 1; }
      checks[$style]+="$(echo "$json" | micros checkMicros) "
      totals[$style]+="$(echo "$json" | micros totalMicros) "
      parses[$style]+="$(echo "$json" | micros parseMicros) "
    done
  done
  for style in a b; do
    printf '%-6s %-5s %10s %10s %10s %10s  %s  runs=%s\n' "$n" "$style" \
      "$(printf '%s\n' ${checks[$style]} | median)" "$(printf '%s\n' ${totals[$style]} | median)" \
      "$(printf '%s\n' ${parses[$style]} | median)" "$(wc -c < "check-$style-$n.ef")" "$load" "${checks[$style]% }"
  done
  unset checks totals parses
done

echo
echo "== Go binary (one call site per type; stencils = text symbols main.efFunction_sort{2,3}[go.shape.*];"
echo "   dicts = main..dict.efFunction_sort* instantiation dictionaries; runtime = SH1's runtime stencil count)"
printf '%-6s %-9s %-5s %10s %8s %6s %8s %10s\n' types layout style bytes stencils dicts runtime stencilB
for types in 16 64; do
  for layout in distinct shared; do
    for style in a b; do
      base="go-$style-$layout-$types"
      node "$here/gen.mjs" --style "$style" --sites "$types" --types "$types" --layout "$layout" > "$base.ef"
      "$EF" build "$base.ef" --target go -o "$base.bin" > /dev/null || { echo "build failed: $base"; exit 1; }
      [ "$("./$base.bin")" = "$types" ] || { echo "wrong output: $base"; exit 1; }
      toolchain=$(go version "$base.bin" | cut -d" " -f2)
      nm=$(go tool nm -size "$base.bin")
      stencils=$(echo "$nm" | awk '($3=="T"||$3=="t") && $4 ~ /^main\.efFunction_sort[23]\[go\.shape\./' | wc -l)
      stencilB=$(echo "$nm" | awk '($3=="T"||$3=="t") && $4 ~ /^main\.efFunction_sort[23]\[go\.shape\./ {s+=$2} END {print s+0}')
      dicts=$(echo "$nm" | awk '$4 ~ /^main\.\.dict\.efFunction_sort[23]\[/' | wc -l)
      runtime=$(echo "$nm" | awk '($3=="T"||$3=="t") && $4 ~ /^ergo\.generated\/runtime\./ && /\[go\.shape\./' | wc -l)
      printf '%-6s %-9s %-5s %10s %8s %6s %8s %10s\n' "$types" "$layout" "$style" "$(wc -c < "$base.bin")" "$stencils" "$dicts" "$runtime" "$stencilB"
    done
  done
done

echo
echo "Go binaries built by ergo with $toolchain"
echo
echo "== JavaScript output (types=16; bytes of the .mjs, raw and gzip -9)"
printf '%-6s %-5s %10s %10s %10s\n' N style mjs gzip d.mts
for n in 16 1000; do
  for style in a b; do
    base="js-$style-$n"
    node "$here/gen.mjs" --style "$style" --sites "$n" --types 16 > "$base.ef"
    "$EF" build "$base.ef" --target js -o "$base.mjs" > /dev/null || { echo "build failed: $base"; exit 1; }
    printf '%-6s %-5s %10s %10s %10s\n' "$n" "$style" "$(wc -c < "$base.mjs")" "$(gzip -9 -c "$base.mjs" | wc -c)" "$(wc -c < "$base.d.mts")"
  done
done
