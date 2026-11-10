#!/bin/bash
# Simulate baton.qnt (X5): the design keeps `safe` and reaches each
# witness; each mutant breaks the invariant it targets. quint run is a
# random simulation (no Apalache: the host has no Java), so "ok" is "no
# violation in the samples", not a proof.
set -u
cd "$(dirname "$0")" || exit 2
Q="npx -y @informalsystems/quint@0.33.1"
status=0
timeout 300 $Q typecheck baton.qnt || { echo "typecheck failed"; exit 2; }
run() {
  local main=$1 inv=$2 want=$3 samples=$4 got
  got=$(timeout 1800 $Q run baton.qnt --main="$main" --invariant="$inv" \
    --max-samples="$samples" --max-steps=30 --seed=11 2>&1 | grep -oE '^\[(ok|violation)\]' | head -1)
  echo "$main $inv: $got (want [$want])"
  [ "$got" = "[$want]" ] || status=1
}
run design safe ok 20000
for w in noDeadlock noTick noEnd; do
  run design "$w" violation 20000
done
run twoBatons OneBaton violation 20000
run earlyTick ClockOnlyWhenIdle violation 20000
run lostWake DeadlockHonest violation 20000
echo "MODEL_STATUS=$status"
exit $status
