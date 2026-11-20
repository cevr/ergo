#!/bin/sh
# The gate step of hosts/opentui, a Go module of its own that the
# repository gate (scripts/gate.sh) does not reach. The batch gate runs it
# after gate.sh --merge, on Linux and on the Mac. It needs the OpenTUI
# library of the host (go run ./cmd/fetch: the network once) and purego in
# the module cache.
#
# It tests the module uncached, then vets it for bindc's four targets with
# no cgo.
set -eu
cd "$(dirname "$0")"
export GOFLAGS="${GOFLAGS:--mod=readonly}"
go test -count=1 -timeout=15m ./...
for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64; do
	GOOS=${target%/*} GOARCH=${target#*/} CGO_ENABLED=0 go vet ./...
done
echo "hosts/opentui: ok"
