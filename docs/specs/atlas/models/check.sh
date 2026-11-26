#!/bin/sh
set -eu

ERGO="${1:-ergo}"
ROOT="$(CDPATH= cd -- "$(dirname "$0")" && pwd)"
LOGDIR="${HARNESS_MODEL_LOG_DIR:-/tmp/atlas-model-logs}"
mkdir -p "$LOGDIR"
export ERGO_FALLBACK=off

for m in turn session limit request rotation wake boardhost bounds direct pool kernel tui restart cutover; do
  log="$LOGDIR/$m.check.log"
  if ! "$ERGO" test "$ROOT/$m" >"$log" 2>&1; then
    echo "FAIL $m (see $log)"
    cat "$log"
    exit 1
  fi
  if grep -Eq '^(FAIL|skip)' "$log"; then
    echo "FAIL/SKIP in $m (see $log)"
    cat "$log"
    exit 1
  fi
  pass_line=$(grep -E '^PASS \(' "$log" | tail -n 1)
  graph=$(grep -oE 'proved: [^)]+' "$log" | head -n 1 || true)
  echo "$m: $pass_line ($graph)"
done
