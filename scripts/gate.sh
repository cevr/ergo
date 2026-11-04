#!/bin/sh
# The merge gate. The runner (scripts/gate) owns the check graph; see its
# package comment. Flags: --list, --no-cache, --merge, --record-durations.
set -eu
cd "$(dirname "$0")/.."
# Go builds stay on disk: /tmp is tmpfs on the workbox, so its default work
# directories fill RAM under parallel gates.
export GOCACHE="${GOCACHE:-$HOME/.cache/go-build}"
export GOTMPDIR="${GOTMPDIR:-$HOME/.cache/go-tmp}"
mkdir -p "$GOTMPDIR"
exec go run ./scripts/gate "$@"
