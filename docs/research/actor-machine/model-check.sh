#!/bin/bash
# Simulate actor_machine.qnt: the design keeps `safe` with 2 and 3 senders
# and reaches each witness; each mutant breaks the invariant it targets.
# quint run is a random simulation (no Apalache: the host has no Java), so
# "ok" is "no violation in the samples", not a proof.
set -u
cd /home/exedev/Developer/personal/.worktrees/ergo-b13-mu3/docs/research/actor-machine || exit 2
Q="npx -y @informalsystems/quint@0.33.1"
status=0
timeout 300 $Q typecheck actor_machine.qnt || { echo "typecheck failed"; exit 2; }
run() {
  local main=$1 inv=$2 want=$3 samples=$4 got
  got=$(timeout 1800 $Q run actor_machine.qnt --main="$main" --invariant="$inv" \
    --max-samples="$samples" --max-steps=40 --seed=11 2>&1 | grep -oE '^\[(ok|violation)\]' | head -1)
  echo "$main $inv: $got (want [$want])"
  [ "$got" = "[$want]" ] || status=1
}
run design2 safe ok 50000
run design3 safe ok 50000
for w in noStaleTick noEndAfterAsks noFullInbox noTwoSendersQueued; do
  run design2 "$w" violation 50000
done
run noEpoch noStaleStep violation 50000
run armEarly exitBeforeEntry violation 50000
run stayRestarts oneTimerPerEntry violation 50000
run noClose noStepAfterEnd violation 50000
run answerTwice askAnsweredOnce violation 50000
run twoAtOnce runToCompletion violation 50000
run anyOrder perSenderOrder violation 50000
echo "MODEL_STATUS=$status"
exit $status
