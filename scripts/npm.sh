#!/bin/sh
# npm.sh NAME [--pack DIR | --publish]: build and pack the binary package
# NAME of tools/npm (its line in tools/npm/packages): the static binary of
# PROGRAM/cmd/NAME for each platform package (CGO_ENABLED=0), the shared
# launcher into a wrapper that takes it, then each package's tarball. With
# no flag it publishes nothing: `npm pack --dry-run` lists each tarball's
# files and sizes. --pack DIR writes the tarballs into DIR. --publish runs
# npm publish (the owner's step).
# ERGO is the ergo binary (default: ergo on PATH).
set -eu
name=${1:?usage: npm.sh NAME [--pack DIR | --publish]}
mode=${2:-}
packdir=${3:-}
[ "$mode" != --pack ] || [ -n "$packdir" ] || { echo "npm.sh: --pack needs a folder" >&2; exit 2; }
root=$(cd "$(dirname "$0")/.." && pwd)
ergo=${ERGO:-ergo}
npm=$root/tools/npm
line=$(grep -v '^#' "$npm/packages" | awk -v n="$name" '$1 == n')
[ -n "$line" ] || { echo "npm.sh: no package $name in tools/npm/packages" >&2; exit 2; }
set -- $line
program=$2 launcher=$3
for plat in linux/amd64:linux-x64 linux/arm64:linux-arm64 darwin/arm64:darwin-arm64; do
	target=${plat%%:*}
	dir=$npm/$name-${plat#*:}
	mkdir -p "$dir/bin"
	if [ "$name" = work ]; then
		CGO_ENABLED=0 ERGO="$ergo" sh "$root/scripts/build-work.sh" "$dir/bin/$name" --platform "$target"
	else
		(cd "$root/$program" && CGO_ENABLED=0 "$ergo" build "./cmd/$name" --platform "$target" -o "$dir/bin/$name")
	fi
	chmod 755 "$dir/bin/$name"
done
if [ "$launcher" = shared ]; then
	mkdir -p "$npm/$name/bin"
	cp "$npm/launcher/launcher.sh" "$npm/$name/bin/$name"
	chmod 755 "$npm/$name/bin/$name"
	cp "$npm/launcher/install.js" "$npm/$name/install.js"
fi
for pkg in "$name-linux-x64" "$name-linux-arm64" "$name-darwin-arm64" "$name"; do
	case "$mode" in
	--publish) (cd "$npm/$pkg" && npm publish --access public) ;;
	--pack) (cd "$npm/$pkg" && npm pack --pack-destination "$packdir") ;;
	*) (cd "$npm/$pkg" && npm pack --dry-run) ;;
	esac
done
