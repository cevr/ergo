#!/bin/sh
# heavy.sh: run a heavy command (a gate, the tests of a big package, a cold build) on the host's run budget, `ergo
# budget` (docs/tooling.md, "ergo budget"), in a systemd user unit of its own that outlives its caller. A host runs
# it through a thin script of its own that sets the variables below and execs this file; install a new copy of
# either by a rename, never a copy in place, since a running sh reads its script by offset.
#
#   heavy.sh COMMAND [ARGS...]   run it; prints its output and returns its exit code
#   heavy.sh wait NAME           wait for run NAME to end; prints its output and returns its exit code
#
#   ERGO_BUDGET         the budget file (required).
#   ERGO_BUDGET_BIN     the ergo that runs `ergo budget` (default: ergo on PATH).
#   ERGO_BUDGET_CRIT    a file of workspace names, one a line, whose runs take the crit class; every other run is
#                       the lane class. Crit is the host owner's to grant: no variable of the caller's grants it.
#   ERGO_BUDGET_SCRATCH the root of the runs' own TMPDIR and GOTMPDIR folders (default $TMPDIR/ergo-budget;
#                       scratch.sh): each run's is removed when it ends or its unit stops, a stray one by a sweep.
#   HEAVY_DIR           the folder of the runs' logs, exit codes and locks (default: $ERGO_BUDGET.heavy).
#   HEAVY_NAME          the run's name (default: made from the command and the time).
#   HEAVY_MEM           the unit's memory cap (default 24G, no swap).
#   HEAVY_WARM_GOCACHE  the warm Go build cache (default ~/.cache/ergo-warm-gocache); HEAVY_WARM_CAP_KIB its size
#                       past which it restarts (default 10 GiB); HEAVY_COLD=1 skips the seed.
#
# Each run is the transient unit heavy-NAME, whose first stderr line names it: a caller that dies reattaches with
# `heavy.sh wait NAME`. The run sees the caller's environment, umask and directory; its output goes to
# $HEAVY_DIR/runs/NAME.log, given to the caller when it ends.
#
# Width. A run takes as many cores as it declares: HEAVY_CORES, else GOMAXPROCS, else ERGO_GATE_CPUS, else the -p
# of a go test, else 4; at most the budget's cores. Its lane is heavy:<the basename of its git top level, else of
# its folder>. A heavy.sh inside a run (HEAVY_HELD=1) runs its command at once, and an `ergo budget` inside a run
# runs in the run's grant: the run already holds its cores.
#
# Warm build cache. A run whose GOCACHE is set but missing or empty starts from a reflink copy of the warm cache;
# after the run its new files are reflinked back. Go's cache is content-keyed, so a seed changes no result. An
# unset GOCACHE is left alone.
[ -n "${ERGO_BUDGET:-}" ] || { echo "heavy.sh: ERGO_BUDGET names no budget file" >&2; exit 2; }
D=${HEAVY_DIR:-$ERGO_BUDGET.heavy}
R=$D/runs
W=${HEAVY_WARM_GOCACHE:-$HOME/.cache/ergo-warm-gocache}
CAP=${HEAVY_WARM_CAP_KIB:-10485760}
SELF=$(readlink -f "$0")
. "$(dirname "$SELF")/scratch.sh"
mkdir -p "$R"

# quote: $1 as one sh word.
quote() { printf "'%s'" "$(printf '%s' "$1" | sed "s/'/'\\\\''/g")"; }

# active: the state of unit heavy-$1 is not yet over (starting, running or stopping).
active() {
  case "$(systemctl --user show -p ActiveState --value "heavy-$1" 2>/dev/null)" in
    active | activating | deactivating | reloading) return 0 ;;
  esac
  return 1
}

# wait_run: wait for run $1 to end, print its output and return its exit code. The run holds $R/$1.lock for its
# whole life, so flock blocks until it ends; the loop only covers a unit that has started but not yet locked.
wait_run() {
  while :; do
    flock "$R/$1.lock" true
    active "$1" || break
    sleep 1
  done
  [ -f "$R/$1.log" ] && cat "$R/$1.log"
  if [ -s "$R/$1.exit" ]; then
    return "$(cat "$R/$1.exit")"
  fi
  echo "heavy.sh: run $1 left no exit code (unknown run, or its unit never started)" >&2
  return 1
}

# post: the unit's ExecStopPost. Records an exit code the run could not (a kill).
post() {
  if [ ! -s "$R/$1.exit" ]; then
    case "${EXIT_CODE:-}:${EXIT_STATUS:-}" in
      exited:*) echo "${EXIT_STATUS}" ;;
      *:KILL) echo 137 ;;
      *:TERM) echo 143 ;;
      *:INT) echo 130 ;;
      *) echo 129 ;;
    esac > "$R/$1.exit"
  fi
  rm -f "$R/$1.env"
  scratch_dir=$scratch_root/heavy-$1
  scratch_drop
}

# seed: reflink the warm cache into a missing or empty $GOCACHE.
seed() {
  [ -n "${GOCACHE:-}" ] && [ "${HEAVY_COLD:-0}" != 1 ] && [ "$GOCACHE" != "$W" ] && [ -d "$W" ] || return 0
  if [ -d "$GOCACHE" ] && [ -n "$(ls -A "$GOCACHE" 2>/dev/null)" ]; then return 0; fi
  tmp=$GOCACHE.seed-$$
  mkdir -p "$(dirname "$GOCACHE")"
  if flock -s "$W.lock" cp -a --reflink=always "$W" "$tmp" 2>/dev/null; then
    rmdir "$GOCACHE" 2>/dev/null
    mv "$tmp" "$GOCACHE" && echo "heavy.sh: seeded $GOCACHE from $W" >&2
  else
    rm -rf "$tmp"
  fi
}

# keep: reflink the run's new cache files into the warm cache; restart it from the run's cache past CAP.
keep() {
  [ -n "${GOCACHE:-}" ] && [ "$GOCACHE" != "$W" ] && [ -d "$GOCACHE" ] || return 0
  (
    flock 7
    if [ ! -d "$W" ] || [ "$(du -sk "$W" 2>/dev/null | cut -f1)" -gt "$CAP" ]; then
      rm -rf "$W.old"
      [ -d "$W" ] && mv "$W" "$W.old"
      cp -a --reflink=always "$GOCACHE" "$W" 2>/dev/null || rm -rf "$W"
      rm -rf "$W.old"
    else
      cp -a -n --reflink=always "$GOCACHE/." "$W/" 2>/dev/null
    fi
  ) 7>"$W.lock"
  return 0
}

# cores: the cores the command "$@" declares (see Width above).
cores() {
  c=${HEAVY_CORES:-${GOMAXPROCS:-${ERGO_GATE_CPUS:-}}}
  if [ -z "$c" ]; then
    prev=
    for a in "$@"; do
      case "$prev:$a" in
        -p:*) c=$a ;;
        *:-p=*) c=${a#-p=} ;;
      esac
      prev=$a
    done
  fi
  case "$c" in "" | *[!0-9]* | 0) c=4 ;; esac
  echo "$c"
}

# run: inside unit heavy-$1, take the cores from the budget and run the command the env file names.
run() {
  name=$1
  exec 7>"$R/$name.lock"
  flock 7
  . "$R/$name.env"
  rm -f "$R/$name.env"
  unset INVOCATION_ID JOURNAL_STREAM SYSTEMD_EXEC_PID NOTIFY_SOCKET MANAGERPID
  exec >"$R/$name.log" 2>&1
  ws=$(basename "$(git rev-parse --show-toplevel 2>/dev/null || pwd)")
  cls=lane
  if [ -n "${ERGO_BUDGET_CRIT:-}" ] && grep -qx "$ws" "$ERGO_BUDGET_CRIT" 2>/dev/null; then cls=crit; fi
  mine=$(cores "$@")
  max=$(awk '$1 == "cores" { print $2 }' "$ERGO_BUDGET")
  case "$max" in "" | *[!0-9]*) ;; *) [ "$mine" -gt "$max" ] && mine=$max ;; esac
  trap 'exit 143' TERM
  trap 'exit 130' INT
  seed
  scratch_make "heavy-$name" || echo "heavy.sh: no scratch folder under $scratch_root; TMPDIR stays the caller's" >&2
  export HEAVY_HELD=1
  "${ERGO_BUDGET_BIN:-ergo}" budget --config "$ERGO_BUDGET" --width "$mine" --class "$cls" --lane "heavy:$ws" -- "$@"
  c=$?
  echo "$c" > "$R/$name.exit"
  scratch_drop
  keep
  exit $c
}

case "${1:-}" in
  wait)
    [ -n "${2:-}" ] || { echo "usage: heavy.sh wait NAME" >&2; exit 2; }
    wait_run "$2"
    exit $?
    ;;
  __post) post "$2"; exit 0 ;;
  __run) shift; run "$@" ;;
esac
[ $# -gt 0 ] || { echo "usage: heavy.sh COMMAND [ARGS...] | heavy.sh wait NAME" >&2; exit 2; }
[ "${HEAVY_HELD:-0}" = 1 ] && exec "$@"

name=${HEAVY_NAME:-$(printf '%s-' "$@" | tr -c 'A-Za-z0-9_.-' '-' | tr -s '-' | cut -c1-40 | sed 's/-*$//')-$(date +%d%H%M%S)-$$}
case "$name" in "" | *[!A-Za-z0-9_.-]*) echo "heavy.sh: HEAVY_NAME '$name' must be letters, digits, _ . -" >&2; exit 2 ;; esac
if active "$name"; then
  echo "heavy.sh: heavy-$name is already running; wait for it with: sh $SELF wait $name" >&2
  exit 2
fi
find "$R" -maxdepth 1 -type f -mtime +2 -delete 2>/dev/null
scratch_sweep
rm -f "$R/$name.exit" "$R/$name.log"
mask=$(umask)
(
  umask 077
  {
    export -p
    echo "umask $mask"
    printf 'set --'
    for a in "$@"; do printf ' %s' "$(quote "$a")"; done
    echo
  } > "$R/$name.env"
)
echo "heavy.sh: run heavy-$name ($(cores "$@") cores); if this caller ends first: sh $SELF wait $name" >&2
if ! systemd-run --user -q --collect --unit="heavy-$name" --same-dir \
  -p MemoryMax="${HEAVY_MEM:-24G}" -p MemorySwapMax=0 \
  -E ERGO_BUDGET="$ERGO_BUDGET" -E ERGO_BUDGET_SCRATCH="$scratch_root" -E HEAVY_DIR="$D" \
  -E GIT_CONFIG_COUNT=2 -E GIT_CONFIG_KEY_0=gc.auto -E GIT_CONFIG_VALUE_0=0 -E GIT_CONFIG_KEY_1=maintenance.auto -E GIT_CONFIG_VALUE_1=false \
  -p "ExecStopPost=/bin/sh $SELF __post $name" \
  /bin/sh "$SELF" __run "$name"; then
  rm -f "$R/$name.env"
  echo "heavy.sh: systemd-run could not start heavy-$name" >&2
  exit 2
fi
wait_run "$name"
