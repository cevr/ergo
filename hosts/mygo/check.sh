#!/bin/sh
# The gate step of hosts/mygo, a Go module of its own that the repository
# gate (scripts/gate.sh) does not reach. The batch gate runs it after
# gate.sh --merge, on Linux and on the Mac. It needs MyGo's pinned module
# and Go 1.27.3 in the module cache: the network once, then none.
#
# It tests both modules (the drawing layer, whose tests check that its
# files are what drawgen writes and mirror MyGo's internal packages, then
# the host) uncached, and vets both for every desktop target and the
# browser, where the generated files differ by build constraint.
set -eu
cd "$(dirname "$0")"
export GOFLAGS="${GOFLAGS:--mod=readonly}"
for dir in drawing .; do
	go -C "$dir" test -count=1 -timeout=15m ./...
	for target in linux/amd64 linux/arm64 darwin/arm64 windows/amd64 js/wasm; do
		GOOS=${target%/*} GOARCH=${target#*/} CGO_ENABLED=0 go -C "$dir" vet ./...
	done
done
echo "hosts/mygo: ok"
