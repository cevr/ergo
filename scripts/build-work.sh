#!/bin/sh
# Build a Work executable stamped with the checkout revision and UTC build date.
# Usage: sh scripts/build-work.sh ABSOLUTE-OUTPUT [ergo build flags...]
set -eu
out=${1:?usage: build-work.sh ABSOLUTE-OUTPUT [ergo build flags...]}
shift
case "$out" in /*) ;; *) echo "build-work.sh: specify an absolute output path" >&2; exit 2 ;; esac
root=$(cd "$(dirname "$0")/.." && pwd -P)
test -f "$root/examples/work/cmd/work/version.txt"
commit=$(git -C "$root" rev-parse --verify HEAD)
changed=$(git -C "$root" status --porcelain -- examples/work modules/work)
if [ -n "$changed" ]; then commit="$commit (modified sources)"; fi
built=$(date -u '+%Y-%m-%dT%H:%M:%SZ')
tmp=$(cd "${TMPDIR:-/tmp}" && pwd -P)
scratch=$(mktemp -d "$tmp/work-stamp.XXXXXX")
case "$scratch" in "$tmp"/work-stamp.*) ;; *) exit 1 ;; esac
trap 'rm -rf -- "$scratch"' EXIT
trap 'exit 1' HUP INT TERM
mkdir "$scratch/examples" "$scratch/modules"
cp -R "$root/examples/work" "$scratch/examples/work"
cp -R "$root/modules/work" "$scratch/modules/work"
printf 'work %s built %s\n' "$commit" "$built" > "$scratch/examples/work/cmd/work/version.txt"
cd "$scratch/examples/work" || exit 1
"${ERGO:-ergo}" build ./cmd/work -o "$out" "$@"
