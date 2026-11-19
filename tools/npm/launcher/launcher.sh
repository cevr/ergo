#!/bin/sh
# The launcher of a binary package of tools/npm (scripts/npm.sh copies it
# to bin/NAME of a wrapper package that names it in tools/npm/packages).
# Until install.js runs (an install with --ignore-scripts), this finds the
# platform package's binary and runs it in place of itself. install.js
# replaces this file with the binary, so the bin is the native program
# and no shell or Node process starts before it.
# NAME is this file's own name; the platform packages are @cevr/NAME-OS-CPU.
# $0 is the link a package manager made (node_modules/.bin/NAME, a global
# bin): follow it to this file first, so the search starts at the package
# (readlink in a loop: macOS before 12.3 has no readlink -f).
self=$0
while [ -h "$self" ]; do
	link=$(readlink "$self")
	case "$link" in
	/*) self=$link ;;
	*) self=$(dirname "$self")/$link ;;
	esac
done
here=$(dirname "$self")
name=$(basename "$self")
case "$(uname -s)-$(uname -m)" in
Linux-x86_64) pkg=$name-linux-x64 ;;
Linux-aarch64 | Linux-arm64) pkg=$name-linux-arm64 ;;
Darwin-arm64) pkg=$name-darwin-arm64 ;;
*)
	echo "$name: no prebuilt binary for $(uname -s) $(uname -m)" >&2
	exit 1
	;;
esac
for dir in "$here/../node_modules/@cevr/$pkg" "$here/../../$pkg" "$here/../../../@cevr/$pkg"; do
	if [ -x "$dir/bin/$name" ]; then
		exec "$dir/bin/$name" "$@"
	fi
done
echo "$name: the package @cevr/$pkg is not installed (an install without optional dependencies)" >&2
exit 1
