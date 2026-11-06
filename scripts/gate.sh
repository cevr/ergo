#!/bin/sh
# The repository gate. The runner (scripts/gate) owns the check graph; see its
# package comment. The default run is the fast tier a lane hands back on;
# --full is the full tier and --merge the merge tier, which every batch head
# passes before main. Flags: --list, --full, --no-cache, --merge,
# --record-durations.
set -eu
cd "$(dirname "$0")/.."
# Go builds stay on disk: /tmp is tmpfs on the workbox, so its default work
# directories fill RAM under parallel gates.
export GOCACHE="${GOCACHE:-$HOME/.cache/go-build}"
export GOTMPDIR="${GOTMPDIR:-$HOME/.cache/go-tmp}"
mkdir -p "$GOTMPDIR"
# The gate is built, then executed in place of this shell, so a signal sent
# to this script's process reaches the gate's stop code (`go run` does not
# pass SIGTERM on). go build relinks only when the gate's sources changed
# (Go's build cache), and each checkout has its own binary, so a gate
# running from one checkout is never rewritten by another's build.
cache="${ERGO_GATE_CACHE:-${XDG_CACHE_HOME:-$HOME/.cache}/ergo-gate}"
checkout=$(pwd | cksum | cut -d ' ' -f 1)
bin="$cache/bin/$checkout/gate"
mkdir -p "$(dirname "$bin")"
go build -buildvcs=false -o "$bin" ./scripts/gate
exec "$bin" "$@"
