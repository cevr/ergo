#!/bin/sh
# npmwork.sh [--publish]: build the work binary for each platform package
# of tools/npm (static: CGO_ENABLED=0) and pack the four packages. With no
# flag it publishes nothing: `npm pack --dry-run` lists each tarball's
# files and sizes. --publish runs npm publish (the owner's step).
# ERGO is the ergo binary (default: ergo on PATH).
# The steps are scripts/npm.sh's, shared by every package of tools/npm.
exec sh "$(dirname "$0")/npm.sh" work "$@"
