#!/bin/sh
# npmwork-check.sh SCRATCH: the npm packages' launcher with no postinstall
# (the verifier's M3 of work r2). It builds the work binary for this host
# into its platform package, packs @cvr/work and that package into
# SCRATCH (a folder that does not exist yet), and installs the tarballs
# three ways that run no postinstall: bun add, bun add -g and
# npm install --ignore-scripts. Each must run `work --help` through the
# link the package manager made. HOME, BUN_INSTALL and the caches are in
# SCRATCH, so no owner config or token is read. It publishes nothing and
# leaves SCRATCH for the caller to delete.
# ERGO is the ergo binary (default: ergo on PATH).
set -eu
S=${1:?usage: npmwork-check.sh SCRATCH}
[ ! -e "$S" ] || { echo "npmwork-check: $S exists" >&2; exit 2; }
root=$(cd "$(dirname "$0")/.." && pwd)
ergo=${ERGO:-ergo}
case "$(uname -s)-$(uname -m)" in
Linux-x86_64) plat=linux/amd64 pkg=work-linux-x64 ;;
Linux-aarch64 | Linux-arm64) plat=linux/arm64 pkg=work-linux-arm64 ;;
Darwin-arm64) plat=darwin/arm64 pkg=work-darwin-arm64 ;;
*) echo "npmwork-check: no platform package for $(uname -s) $(uname -m)" >&2; exit 2 ;;
esac
mkdir -p "$S/home" "$S/cache" "$S/tgz" "$S/bun-cache" "$S/bun-global" "$S/bun" "$S/npm"
S=$(cd "$S" && pwd -P)
export HOME="$S/home" npm_config_cache="$S/cache" npm_config_userconfig="$S/home/.npmrc" npm_config_globalconfig="$S/home/.npmrc-global" npm_config_update_notifier=false npm_config_fund=false npm_config_audit=false BUN_INSTALL_CACHE_DIR="$S/bun-cache"
: > "$S/home/.npmrc"
mkdir -p "$root/tools/npm/$pkg/bin"
(cd "$root/examples/work" && CGO_ENABLED=0 "$ergo" build ./cmd/work --platform "$plat" -o "$root/tools/npm/$pkg/bin/work")
chmod 755 "$root/tools/npm/$pkg/bin/work"
for p in "$pkg" work; do (cd "$root/tools/npm/$p" && npm pack --silent --pack-destination "$S/tgz" >/dev/null); done
tgz_plat=$(ls "$S"/tgz/cvr-"$pkg"-*.tgz)
tgz_work=$(ls "$S"/tgz/cvr-work-[0-9]*.tgz)
failed=0
run() {
	if "$2" --help >"$S/out" 2>&1 && grep -q '^Usage of work' "$S/out"; then
		echo "ok   $1: $2 --help"
	else
		echo "FAIL $1: $2 --help: $(head -n 1 "$S/out")"
		failed=$((failed + 1))
	fi
}
for d in bun npm; do printf '{"name":"check-%s","version":"0.0.0","private":true}\n' "$d" > "$S/$d/package.json"; done
(cd "$S/bun" && bun add "$tgz_plat" "$tgz_work" >"$S/bun.log" 2>&1) || echo "bun add exit $? (see $S/bun.log)"
run "bun add" "$S/bun/node_modules/.bin/work"
(cd "$S/bun" && BUN_INSTALL="$S/bun-global" bun add -g "$tgz_plat" "$tgz_work" >"$S/bun-global.log" 2>&1) || echo "bun add -g exit $? (see $S/bun-global.log)"
run "bun add -g" "$S/bun-global/bin/work"
(cd "$S/npm" && npm install --ignore-scripts --no-save "$tgz_plat" "$tgz_work" >"$S/npm.log" 2>&1) || echo "npm install exit $? (see $S/npm.log)"
run "npm install --ignore-scripts" "$S/npm/node_modules/.bin/work"
if [ "$failed" -eq 0 ]; then
	echo "npmwork-check: passed"
else
	echo "npmwork-check: $failed failed"
	exit 1
fi
