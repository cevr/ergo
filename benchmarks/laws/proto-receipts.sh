#!/usr/bin/env bash
# Regenerates the laws prototype receipts in benchmarks/laws/runs/:
#   erasure-diff.txt  each law example built with and without its contract and
#                     satisfies blocks (replaced by blank lines so line
#                     directives do not move); only the revision header may differ.
#   seed-survey.txt   the broken compare and the seed-sensitive near-order probe
#                     under the default and exploration root seeds on both
#                     targets; exploration receipts have their own path.
# The prototype lives on wf10/spike-laws-proto, which is never merged, so pass
# a checkout of that branch. Exits 1 when an output differs beyond the header,
# a seed run disagrees across targets, or a tool is missing.
# Usage: benchmarks/laws/proto-receipts.sh <proto-checkout> [scratch-dir]
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
proto=$(cd "${1:?usage: proto-receipts.sh <proto-checkout> [scratch-dir]}" && pwd)
scratch=${2:-$(mktemp -d)}
for tool in go jq awk diff; do command -v "$tool" > /dev/null || { echo "missing $tool" >&2; exit 2; }; done
[ -f "$proto/examples/laws/order-broken.ef" ] || { echo "$proto is not a wf10/spike-laws-proto checkout" >&2; exit 2; }
mkdir -p "$scratch"
ef="$scratch/ef"
(cd "$proto" && go build -o "$ef" ./cmd/ergo)
head=$(git -C "$proto" rev-parse --short HEAD)
fail=0

work="$scratch/erase"
rm -rf "$work"
mkdir -p "$work/with" "$work/without"
out="$here/runs/erasure-diff.txt"
echo "# proto $head: Go main.go, JavaScript module and .d.mts, with laws vs laws replaced by blank lines" > "$out"
for name in codec order-broken; do
  cp "$proto/examples/laws/$name.ef" "$work/with/$name.ef"
  awk '/^contract .*\{$/||/ satisfies .*\{$/{skip=1} {if(skip)print ""; else print} skip&&/^\}$/{skip=0}' \
    "$proto/examples/laws/$name.ef" > "$work/without/$name.ef"
  if grep -q 'satisfies\|^contract ' "$work/without/$name.ef"; then echo "FAIL strip left law syntax in $name"; fail=1; fi
  for side in with without; do
    (cd "$work/$side" && "$ef" build "$name.ef" > /dev/null 2>&1 && "$ef" build --target js "$name.ef" > /dev/null 2>&1)
    manifest=$(grep -l "/$name.ef\"" "$work/$side"/dist/go/apps/*/generations/.stage-*/.ergo-output.json | head -1)
    cp "$(dirname "$manifest")/main.go" "$work/$side/$name.main.go"
  done
  for pair in "Go main.go:$name.main.go" "JavaScript mjs:dist/$name.mjs" "JavaScript d.mts:dist/$name.d.mts"; do
    label=${pair%%:*}
    file=${pair#*:}
    echo "== diff $name.ef $label" >> "$out"
    diff "$work/with/$file" "$work/without/$file" >> "$out" || true
    if ! diff <(grep -v 'Source revision' "$work/with/$file") <(grep -v 'Source revision' "$work/without/$file") > /dev/null; then
      echo "FAIL $name.ef $label differs beyond the revision header"; fail=1
    fi
  done
done

runs="$scratch/seeds"
rm -rf "$runs"
mkdir -p "$runs"
cp "$proto/examples/laws/order-broken.ef" "$proto/examples/laws/probes/near.ef" "$runs/"
out="$here/runs/seed-survey.txt"
echo "# ergo test --target both <program> per ERGO_LAW_SEED (proto $head); cases = cases run before the verdict (budget 200)." > "$out"
echo "# The default run writes the gating receipt dist/<name>.laws.<target>.json; an exploration seed writes dist/<name>.laws.<target>.seed-N.json and never gates." >> "$out"
survey() {
  local name=$1 seed receipt
  shift
  echo "== $name.ef" >> "$out"
  for seed in "$@"; do
    if [ "$seed" = default ]; then
      (cd "$runs" && "$ef" test --target both "$name.ef" > seed.out 2> /dev/null) || true
      receipt="$runs/dist/$name.laws.go.json"
    else
      (cd "$runs" && ERGO_LAW_SEED=$seed "$ef" test --target both "$name.ef" > seed.out 2> /dev/null) || true
      receipt="$runs/dist/$name.laws.go.seed-$seed.json"
    fi
    [ -f "$receipt" ] || { echo "FAIL $name seed $seed: no receipt at $receipt"; fail=1; continue; }
    agree=$(jq -r 'select(.lawComparison) | .lawComparison.agree' "$runs/seed.out")
    gating=$(jq -r 'select(.lawComparison) | .lawComparison.gating' "$runs/seed.out")
    [ "$agree" = true ] || { echo "FAIL $name seed $seed: targets disagree"; fail=1; }
    if [ "$seed" = default ]; then want=true; else want=false; fi
    [ "$gating" = "$want" ] || { echo "FAIL $name seed $seed: gating is $gating"; fail=1; }
    echo "seed=$seed agree=$agree gating=$gating $(jq -r '[.laws[] | (.id | split(".")[1] | split("@")[0]) + ":" + .status + "/" + (.cases | tostring)] | join(" ")' "$receipt")" >> "$out"
  done
  # Exploration seeds never overwrite the gating receipt.
  [ "$(jq -r '.rootSeed' "$runs/dist/$name.laws.go.json")" = 0 ] || { echo "FAIL $name: the gating receipt does not carry root seed 0"; fail=1; }
}
survey order-broken 1 2 3 4 5 6 7 8 default
survey near default 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16

if [ "$fail" -ne 0 ]; then echo "proto receipts: FAILED"; exit 1; fi
echo "proto receipts: ok ($head)"
