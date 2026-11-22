#!/usr/bin/env bash
# Measures the laws prototype's cost (review 1 m5) into
# benchmarks/laws/runs/cost.txt, with the conditions and every raw sample.
#   test     `ergo test` per target on a law program and on its control, the same
#            program with its contract and satisfies blocks blanked; both carry
#            one trivial test_ function so the control has something to run.
#            cold = after a source edit (a new revision), warm = repeated.
#   order    `ergo test` per target on examples/laws/order.ef (eight laws).
#   build    `ergo build` per target on the law program and its control: laws
#            are erased, so the two should cost the same.
#   adapter  cost per exported call in ts/proto/lib.mjs, body included, and the
#            same body with no adapter, whose difference is the adapter (ts/adapter-cost.mjs).
# The prototype lives on wf10/spike-laws-proto, which is never merged, so pass
# a checkout of that branch. Exits 1 when a run fails, 2 when a tool is missing.
# Usage: benchmarks/laws/proto-cost.sh <proto-checkout> [scratch-dir] [samples]
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
proto=$(cd "${1:?usage: proto-cost.sh <proto-checkout> [scratch-dir] [samples]}" && pwd)
scratch=${2:-$(mktemp -d)}
samples=${3:-5}
for tool in go node awk date sort; do command -v "$tool" > /dev/null || { echo "missing $tool" >&2; exit 2; }; done
[ -f "$proto/examples/laws/probes/providers.ef" ] || { echo "$proto is not a wf10/spike-laws-proto checkout" >&2; exit 2; }
mkdir -p "$scratch"
scratch=$(cd "$scratch" && pwd)
ef="$scratch/ef"
(cd "$proto" && go build -o "$ef" ./cmd/ergo)
head=$(git -C "$proto" rev-parse --short HEAD)
out="$here/runs/cost.txt"

work="$scratch/cost"
rm -rf "$work"
mkdir -p "$work/laws" "$work/control" "$work/order"
control_test='
effect fn test_control() -> void raises { AssertionFailed } uses { Assert } {
    run Assert.check(true, "control")
}'
cp "$proto/examples/laws/probes/providers.ef" "$work/laws/cost.ef"
awk '/^contract .*\{$/||/ satisfies .*\{$/{skip=1} {if(skip)print ""; else print} skip&&/^\}$/{skip=0}' \
  "$proto/examples/laws/probes/providers.ef" > "$work/control/cost.ef"
if grep -q 'satisfies\|^contract ' "$work/control/cost.ef"; then echo "strip left law syntax" >&2; exit 1; fi
printf '%s\n' "$control_test" >> "$work/laws/cost.ef"
printf '%s\n' "$control_test" >> "$work/control/cost.ef"
cp "$proto/examples/laws/order.ef" "$work/order/order.ef"

now() { date +%s%N; }
# sample <dir> <cmd...>: seconds for one run; exits on failure.
sample() {
  local dir=$1 start end status
  shift
  start=$(now)
  set +e
  (cd "$dir" && "$@" > "$work/last.out" 2>&1)
  status=$?
  set -e
  end=$(now)
  if [ "$status" -ne 0 ]; then
    echo "FAIL ($dir) $*: exit $status" >&2
    head -c 2000 "$work/last.out" >&2
    exit 1
  fi
  awk -v d=$((end - start)) 'BEGIN { printf "%.3f", d / 1e9 }'
}
median() { tr ' ' '\n' | sort -n | awk '{ v[NR] = $1 } END { print v[int((NR + 1) / 2)] }'; }
# series <label> <dir> <file> <edit:yes|no> <cmd...>: prints the label, median and raw samples.
series() {
  local label=$1 dir=$2 file=$3 edit=$4 raw="" i s
  shift 4
  for i in $(seq 1 "$samples"); do
    [ "$edit" = yes ] && printf '// cost sample %s %s\n' "$i" "$(now)" >> "$dir/$file"
    s=$(sample "$dir" "$@")
    raw="$raw $s"
  done
  raw=${raw# }
  echo "$label median=$(echo "$raw" | median)s raw=$raw" >> "$out"
}

{
  echo "# laws prototype cost (proto $head), $samples samples per series, seconds of wall time"
  echo "# date $(date -u +%Y-%m-%dT%H:%M:%SZ) host $(uname -srm) cpus $(nproc)"
  echo "# go $(go env GOVERSION) node $(node --version)"
  echo "# load average at start: $(cut -d' ' -f1-3 /proc/loadavg)"
  echo "# law program: examples/laws/probes/providers.ef (two RoundTrip<Account> satisfactions, 200 cases each) plus one test_ function"
  echo "# control: the same file with its contract and satisfies blocks blanked, plus the same test_ function"
} > "$out"

for target in go js; do
  # Prime the shared caches (Go build cache, test binary) once per target.
  sample "$work/laws" "$ef" test --target "$target" cost.ef > /dev/null
  sample "$work/control" "$ef" test --target "$target" cost.ef > /dev/null
  series "test laws $target cold" "$work/laws" cost.ef yes "$ef" test --target "$target" cost.ef
  series "test control $target cold" "$work/control" cost.ef yes "$ef" test --target "$target" cost.ef
  series "test laws $target warm" "$work/laws" cost.ef no "$ef" test --target "$target" cost.ef
  series "test control $target warm" "$work/control" cost.ef no "$ef" test --target "$target" cost.ef
  sample "$work/order" "$ef" test --target "$target" order.ef > /dev/null
  series "order test $target cold" "$work/order" order.ef yes "$ef" test --target "$target" order.ef
  series "order test $target warm" "$work/order" order.ef no "$ef" test --target "$target" order.ef
  series "build laws $target cold" "$work/laws" cost.ef yes "$ef" build --target "$target" cost.ef
  series "build control $target cold" "$work/control" cost.ef yes "$ef" build --target "$target" cost.ef
done
echo "# load average at end: $(cut -d' ' -f1-3 /proc/loadavg)" >> "$out"

echo "== exported call and adapter (node, ts/adapter-cost.mjs, 200000 calls per sample, $samples samples after one warm-up)" >> "$out"
node "$here/ts/adapter-cost.mjs" 200000 "$samples" >> "$out"
echo "proto cost: ok ($head)"
