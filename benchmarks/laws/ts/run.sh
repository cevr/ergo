#!/usr/bin/env bash
# spike-laws TS caller view. For each directory: tsc strict on every
# .mts (each file on its own, because one attack augments globals); node runs
# the consumer and the attacks; every @ts-expect-error is opened to show the
# diagnostic it suppresses, with the declared code; `any` is counted in the
# declarations, the runtime and the probes.
#
# proto/ is the prototype's emitted library (wf10/spike-laws-proto) under the
# four boundary rules: its attacks must each be refused (EF186 or TypeError) or
# be harmless. counterfactual/ is the same library with naive trust: its
# attacks must still reach a wrong answer, so the comparison stays live.
#
# TypeScript is the repository's pinned copy (tools/lib.sh); node >= 22.6 runs
# .mts directly. Exits 1 when any tsc or node run fails, a directive
# suppresses nothing or fires with another code, or an `any` keyword appears.
# Usage: benchmarks/laws/ts/run.sh
set -u
here=$(cd "$(dirname "$0")" && pwd)
. "$here/tools/lib.sh"
echo "== versions"
echo "tsc $(node "$TSC" --version) (<repo>/$TS_DIR)"
echo "node $(node --version)"
echo "effect $(node -p "require('$repo/node_modules/effect/package.json').version")"
for dir in proto counterfactual; do
  echo
  echo "==== $dir"
  cd "$here/$dir" || { fail "missing directory $dir"; continue; }
  sources=$(ls *.mts 2>/dev/null | grep -v "\.d\.mts$")
  [ -n "$sources" ] || fail "no .mts to check in $dir"
  for f in $sources; do
    echo "== tsc $f ($(grep -c '@ts-expect-error' "$f") directives)"
    node "$TSC" "${FLAGS[@]}" "$f"
    check "tsc $dir/$f"
  done
  for f in $(ls consumer.mts attacks*.mts 2>/dev/null); do
    echo "== node $f"
    node "$f"
    check "node $dir/$f"
  done
  echo "== each directive opened"
  for f in $sources; do
    grep -q '@ts-expect-error' "$f" || continue
    node "$here/tools/open-negatives.cjs" . "$f"
    check "directives $dir/$f"
  done
  echo "== any"
  node "$here/tools/count-any.cjs" $(ls ./*.mjs ./*.mts 2>/dev/null)
  check "any keywords $dir"
done
finish
