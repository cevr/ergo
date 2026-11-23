#!/bin/sh
# light.sh COMMAND [ARGS...]: run a light check (a focused test, a vet, a build) on the host's run budget, `ergo
# budget` (docs/tooling.md, "ergo budget"), at width 4 and nice 19. A host runs it through a thin script of its own
# that sets the variables below and execs this file; install a new copy of either by a rename, never a copy in
# place, since a running sh reads its script by offset.
#
#   ERGO_BUDGET       the budget file (required).
#   ERGO_BUDGET_BIN   the ergo that runs `ergo budget` (default: ergo on PATH).
#   ERGO_BUDGET_CRIT  a file of workspace names, one a line, whose runs take the crit class; every other run is the
#                     lane class. Crit is the host owner's to grant: no variable of the caller's grants it.
#   ERGO_BUDGET_QUIET a file holding an epoch second; until then a new run waits (a quiet window).
#   ERGO_BUDGET_CEILING a hard memory ceiling, e.g. 8G, that does not change the admission charge. Set it
#                     only after updating ergo to accept --ceiling; unset or empty passes no flag.
#   ERGO_BUDGET_SCRATCH the root of the runs' own TMPDIR and GOTMPDIR folders (default $TMPDIR/ergo-budget;
#                     scratch.sh): each run's is removed when it ends, a killed run's by the next run's sweep.
#
# The lane is the basename of the caller's git top level ("none" outside a repository): the log's lane column and
# the crit file's names.
[ $# -gt 0 ] || { echo "usage: light.sh COMMAND [ARGS...]" >&2; exit 2; }
[ -n "${ERGO_BUDGET:-}" ] || { echo "light.sh: ERGO_BUDGET names no budget file" >&2; exit 2; }
export GIT_CONFIG_COUNT=2 GIT_CONFIG_KEY_0=gc.auto GIT_CONFIG_VALUE_0=0 GIT_CONFIG_KEY_1=maintenance.auto GIT_CONFIG_VALUE_1=false
if [ -n "${ERGO_BUDGET_QUIET:-}" ]; then
  while q=$(cat "$ERGO_BUDGET_QUIET" 2>/dev/null || echo 0); [ "$(date +%s)" -lt "$q" ]; do sleep 60; done
fi
lane=$(basename "$(git rev-parse --show-toplevel 2>/dev/null)" 2>/dev/null)
lane=${lane:-none}
class=lane
if [ -n "${ERGO_BUDGET_CRIT:-}" ] && grep -qx "$lane" "$ERGO_BUDGET_CRIT" 2>/dev/null; then class=crit; fi
. "$(dirname "$(readlink -f "$0")")/scratch.sh"
scratch_sweep
scratch_make "light-$$-$(date +%s%N)" || { echo "light.sh: cannot make a scratch folder under $scratch_root" >&2; exit 1; }
trap scratch_drop EXIT
# ergo budget runs as a child so the folder is removed after it; a signal to this script is passed on to it. An
# asynchronous command's stdin is /dev/null unless redirected, so it reads this script's stdin through fd 3.
exec 3<&0
set -- -- nice -n 19 "$@"
if [ -n "${ERGO_BUDGET_CEILING:-}" ]; then set -- --ceiling "${ERGO_BUDGET_CEILING:-}" "$@"; fi
unset ERGO_BUDGET_CEILING # The ceiling is a launch option, not an input to the payload's build cache.
"${ERGO_BUDGET_BIN:-ergo}" budget --config "$ERGO_BUDGET" --width 4 --class "$class" --lane "$lane" "$@" <&3 3<&- &
child=$!
exec 3<&-
trap 'kill -TERM $child 2>/dev/null' TERM INT HUP
while :; do
  wait $child
  rc=$?
  kill -0 $child 2>/dev/null || break
done
exit $rc
