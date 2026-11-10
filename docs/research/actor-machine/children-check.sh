#!/bin/bash
# Simulate children.qnt (MU5): the design keeps `safe` and reaches each
# witness; each mutant breaks the invariant it targets. quint run is a
# random simulation (no Apalache: the host has no Java), so "ok" is "no
# violation in the samples", not a proof.
set -u
cd "$(dirname "$0")" || exit 2
Q="npx -y @informalsystems/quint@0.33.1"
status=0
timeout 300 $Q typecheck children.qnt || { echo "typecheck failed"; exit 2; }
run() {
  local main=$1 inv=$2 want=$3 samples=$4 got
  got=$(timeout 1800 $Q run children.qnt --main="$main" --invariant="$inv" \
    --max-samples="$samples" --max-steps=40 --seed=11 2>&1 | grep -oE '^\[(ok|violation)\]' | head -1)
  echo "$main $inv: $got (want [$want])"
  [ "$got" = "[$want]" ] || status=1
}
run design safe ok 50000
for w in noDrop noReentryAfterStops noTwoEditors noDeepChild noRootEndAfterStops; do
  run design "$w" violation 50000
done
run lateOutput NoLateChildOutput violation 50000
run parentFirst ChildrenStopFirst violation 50000
run byKind ChildInsideEntry violation 50000
echo "MODEL_STATUS=$status"
exit $status
