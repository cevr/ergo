#!/bin/sh
# npm-check.sh NAME SCRATCH: a binary package of tools/npm installed the
# ways a user installs it. It builds NAME's binary for this host into its
# platform package (scripts/npm.sh's steps), packs @cvr/NAME and that
# package into SCRATCH (a folder that does not exist yet), and installs
# the tarballs four ways: bun add, bun add -g, npm install (postinstall
# places the binary) and npm install --ignore-scripts (the launcher finds
# it). Each must run `NAME --help` through the link the package manager
# made and print a usage line naming NAME. HOME, BUN_INSTALL and the
# caches are in SCRATCH, so no owner config or token is read. It
# publishes nothing and leaves SCRATCH for the caller to delete.
# ERGO is the ergo binary (default: ergo on PATH).
set -eu
name=${1:?usage: npm-check.sh NAME SCRATCH}
S=${2:?usage: npm-check.sh NAME SCRATCH}
[ ! -e "$S" ] || { echo "npm-check: $S exists" >&2; exit 2; }
root=$(cd "$(dirname "$0")/.." && pwd)
case "$(uname -s)-$(uname -m)" in
Linux-x86_64) plat=linux-x64 ;;
Linux-aarch64 | Linux-arm64) plat=linux-arm64 ;;
Darwin-arm64) plat=darwin-arm64 ;;
*) echo "npm-check: no platform package for $(uname -s) $(uname -m)" >&2; exit 2 ;;
esac
mkdir -p "$S/home" "$S/cache" "$S/tgz" "$S/bun-cache" "$S/bun-global" "$S/bun" "$S/npm" "$S/npm-noscripts"
S=$(cd "$S" && pwd -P)
sh "$root/scripts/npm.sh" "$name" --pack "$S/tgz" >"$S/pack.log" 2>&1 || { echo "npm-check: npm.sh $name failed (see $S/pack.log)"; exit 1; }
export HOME="$S/home" npm_config_cache="$S/cache" npm_config_userconfig="$S/home/.npmrc" npm_config_globalconfig="$S/home/.npmrc-global" npm_config_update_notifier=false npm_config_fund=false npm_config_audit=false BUN_INSTALL_CACHE_DIR="$S/bun-cache"
: > "$S/home/.npmrc"
tgz_plat=$(ls "$S"/tgz/cvr-"$name"-"$plat"-*.tgz)
tgz_main=$(ls "$S"/tgz/cvr-"$name"-[0-9]*.tgz)
failed=0
run() {
	if "$2" --help >"$S/out" 2>&1 && grep -qi "^usage.*$name" "$S/out"; then
		echo "ok   $1: $2 --help"
	else
		echo "FAIL $1: $2 --help: $(head -n 1 "$S/out")"
		failed=$((failed + 1))
	fi
}
for d in bun npm npm-noscripts; do printf '{"name":"check-%s","version":"0.0.0","private":true}\n' "$d" > "$S/$d/package.json"; done
(cd "$S/bun" && bun add "$tgz_plat" "$tgz_main" >"$S/bun.log" 2>&1) || echo "bun add exit $? (see $S/bun.log)"
run "bun add" "$S/bun/node_modules/.bin/$name"
(cd "$S/bun" && BUN_INSTALL="$S/bun-global" bun add -g "$tgz_plat" "$tgz_main" >"$S/bun-global.log" 2>&1) || echo "bun add -g exit $? (see $S/bun-global.log)"
run "bun add -g" "$S/bun-global/bin/$name"
(cd "$S/npm" && npm install --no-save "$tgz_plat" "$tgz_main" >"$S/npm.log" 2>&1) || echo "npm install exit $? (see $S/npm.log)"
run "npm install" "$S/npm/node_modules/.bin/$name"
(cd "$S/npm-noscripts" && npm install --ignore-scripts --no-save "$tgz_plat" "$tgz_main" >"$S/npm-noscripts.log" 2>&1) || echo "npm install --ignore-scripts exit $? (see $S/npm-noscripts.log)"
run "npm install --ignore-scripts" "$S/npm-noscripts/node_modules/.bin/$name"
if [ "$failed" -eq 0 ]; then
	echo "npm-check: passed"
else
	echo "npm-check: $failed failed"
	exit 1
fi
