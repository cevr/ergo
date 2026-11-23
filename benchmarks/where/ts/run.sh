#!/usr/bin/env bash
# spike-where TS caller view. For each option directory: tsc strict on
# the consumer, the negatives and the attacks (each file on its own, because
# the attacks augment globals); node runs the consumer and the attacks; every
# @ts-expect-error is opened to show the diagnostic it suppresses; `any` is
# counted in the declarations and runtime.
#
# TypeScript is the repository's pinned copy (tools/lib.sh); node >= 22.6 runs
# .mts directly. The Effect baseline resolves `effect` from the repository's
# node_modules (4.0.1). Exits 1 when any tsc or node run fails, a directive
# suppresses nothing, an `any` keyword appears, or an option directory has no
# .mts to check or no consumer.mts.
# Usage: benchmarks/where/ts/run.sh [option-dir...]   (default: all)
set -u
here=$(cd "$(dirname "$0")" && pwd)
. "$here/tools/lib.sh"
options=("$@")
[ ${#options[@]} -eq 0 ] && options=(a-dict b-witness b-struct b-cond b-evidence c-comparable baseline-effect)
echo "== versions"
echo "tsc $(node "$TSC" --version) (<repo>/$TS_DIR)"
echo "node $(node --version)"
echo "effect $(node -p "require('$repo/node_modules/effect/package.json').version")"
for option in "${options[@]}"; do
  echo
  echo "==== $option"
  cd "$here/$option" || { fail "missing option directory $option"; continue; }
  sources=$(ls *.mts 2>/dev/null | grep -v "\.d\.mts$")
  [ -n "$sources" ] || fail "no .mts to check in $option"
  [ -f consumer.mts ] || fail "no consumer.mts in $option"
  for f in $sources; do
    echo "== tsc $f ($(grep -c '@ts-expect-error' "$f") directives)"
    node "$TSC" "${FLAGS[@]}" "$f"
    check "tsc $option/$f"
  done
  for f in consumer.mts $(ls attacks*.mts 2>/dev/null); do
    [ -f "$f" ] || continue
    echo "== node $f"
    node "$f"
    check "node $option/$f"
  done
  echo "== each directive opened"
  for f in *.mts; do
    grep -q '@ts-expect-error' "$f" || continue
    node "$here/tools/open-negatives.cjs" . "$f"
    check "directives $option/$f"
  done
  echo "== any"
  node "$here/tools/count-any.cjs" $(ls ./*.mjs ./*.mts 2>/dev/null)
  check "any keywords $option"
done
finish
