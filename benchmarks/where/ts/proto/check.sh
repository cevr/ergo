#!/usr/bin/env bash
# Builds sample.ergo with the prototype compiler (branch wf10/spike-where-proto)
# to JavaScript, then type-checks and runs the TypeScript callers against the
# generated .d.mts. out/ is ignored; `effect` resolves from the repository's
# node_modules; TypeScript is the repository's pinned copy (../tools/lib.sh).
# Exits 1 when the build, any tsc or node run, or a directive check fails, or
# an `any` keyword appears in the generated declarations.
# Usage: check.sh EF_PROTO_BINARY
set -u
here=$(cd "$(dirname "$0")" && pwd)
EF=${1:?usage: check.sh EF_PROTO_BINARY}
. "$here/../tools/lib.sh"
cd "$here" || exit 1
mkdir -p out
echo "== versions"
echo "tsc $(node "$TSC" --version) (<repo>/$TS_DIR)"
echo "node $(node --version)"
echo "== ef build sample.ergo --target js"
# The spike compiler reads .ef: build from a copy with that extension.
cp sample.ergo out/sample.ef
"$EF" build out/sample.ef --target js -o out/sample.mjs >/dev/null
check "ef build" || finish
echo "== generated witness signatures"
grep -E 'function_(sort3|insert|lookup|User_compare|UserId_hash|UserId_equals):' out/sample.d.mts
for f in consumer.mts negatives.mts; do
  echo "== tsc $f"
  node "$TSC" "${FLAGS[@]}" "$f"
  check "tsc $f"
done
echo "== node consumer.mts"
node consumer.mts
check "node consumer.mts"
echo "== each directive opened"
node "$here/../tools/open-negatives.cjs" . negatives.mts
check "directives negatives.mts"
echo "== any"
node "$here/../tools/count-any.cjs" out/sample.d.mts out/sample.mjs consumer.mts negatives.mts
check "any keywords"
finish
