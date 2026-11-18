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
#
# The phones (android/arm64, ios/arm64) need cgo and the phone's C
# compiler: android/arm64 is vetted when CC_ANDROID names the NDK's clang
# for it (aarch64-linux-android26-clang), ios/arm64 when Xcode's xcrun is
# here. Without one the step says "skipped: no toolchain", which is not a
# pass: the phone builds are proved by their own leg.
set -eu
cd "$(dirname "$0")"
export GOFLAGS="${GOFLAGS:--mod=readonly}"
for dir in drawing .; do
	# ergo reads the whole module graph (MyGo's CLI, go.mod's tool, among
	# it), which go test alone does not download.
	go -C "$dir" mod download
	go -C "$dir" test -count=1 -timeout=15m ./...
	for target in linux/amd64 linux/arm64 darwin/arm64 windows/amd64 js/wasm; do
		GOOS=${target%/*} GOARCH=${target#*/} CGO_ENABLED=0 go -C "$dir" vet ./...
	done
	if [ -n "${CC_ANDROID:-}" ]; then
		GOOS=android GOARCH=arm64 CGO_ENABLED=1 CC="$CC_ANDROID" go -C "$dir" vet ./...
	else
		echo "hosts/mygo: $dir: android/arm64 vet skipped: no toolchain (set CC_ANDROID)"
	fi
	if command -v xcrun >/dev/null 2>&1; then
		GOOS=ios GOARCH=arm64 CGO_ENABLED=1 CC="$(xcrun --sdk iphoneos -f clang) -isysroot $(xcrun --sdk iphoneos --show-sdk-path) -target arm64-apple-ios17.0" go -C "$dir" vet ./...
	else
		echo "hosts/mygo: $dir: ios/arm64 vet skipped: no toolchain (no xcrun)"
	fi
done
echo "hosts/mygo: ok"
