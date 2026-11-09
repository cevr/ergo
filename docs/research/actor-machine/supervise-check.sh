#!/bin/bash
# Simulate supervise.qnt (MU7): the design keeps `safe` and reaches each
# witness; each mutant breaks `safe`. quint run is a random simulation (no
# Apalache: the host has no Java), so "ok" is "no violation in the
# samples", not a proof.
set -u
cd "$(dirname "$0")" || exit 2
Q="npx -y @informalsystems/quint@0.33.1"
status=0
timeout 300 $Q typecheck supervise.qnt || { echo "typecheck failed"; exit 2; }
run() {
  local main=$1 inv=$2 want=$3 got
  got=$(timeout 1800 $Q run supervise.qnt --main="$main" --invariant="$inv" \
    --max-samples=50000 --max-steps=40 --seed=11 2>&1 | grep -oE '^\[(ok|violation)\]' | head -1)
  echo "$main $inv: $got (want [$want])"
  [ "$got" = "[$want]" ] || status=1
}
run design safe ok
for w in noRestart noExhaustion noFreshAfterCommits; do
  run design "$w" violation
done
for m in reuseState askUnanswered restartAfterDone budgetOffByOne; do
  run "$m" safe violation
done
echo "MODEL_STATUS=$status"
exit $status
