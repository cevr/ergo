#!/bin/sh
# npmwork.sh [--publish]: build the work binary for each platform package
# of tools/npm (static: CGO_ENABLED=0) and pack the four packages. With no
# flag it publishes nothing: `npm pack --dry-run` lists each tarball's
# files and sizes. --publish runs npm publish (the owner's step).
# ERGO is the ergo binary (default: ergo on PATH).
set -eu
root=$(cd "$(dirname "$0")/.." && pwd)
ergo=${ERGO:-ergo}
npm=$root/tools/npm
for plat in linux/amd64:linux-x64 linux/arm64:linux-arm64 darwin/arm64:darwin-arm64; do
	target=${plat%%:*}
	dir=$npm/work-${plat#*:}
	mkdir -p "$dir/bin"
	(cd "$root/examples/work" && CGO_ENABLED=0 "$ergo" build ./cmd/work --platform "$target" -o "$dir/bin/work")
	chmod 755 "$dir/bin/work"
done
for pkg in work-linux-x64 work-linux-arm64 work-darwin-arm64 work; do
	if [ "${1:-}" = --publish ]; then
		(cd "$npm/$pkg" && npm publish --access public)
	else
		(cd "$npm/$pkg" && npm pack --dry-run)
	fi
done
