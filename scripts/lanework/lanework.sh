#!/bin/sh
# lanework.sh: Ergo lanes in copy-on-write workspaces of `work` (ergo work).
#
#   lanework.sh create <branch> <base>   make a lane workspace for <branch>; prints its path
#   lanework.sh env [lane]               print the lane's env (the shared GOCACHE, go.mod's Go first on PATH)
#   lanework.sh push [lane]              hand the branch back to the main checkout
#   lanework.sh remove <lane> [--force]  move the lane to the work trash (at READY)
#   lanework.sh warm                     rebuild the shared Go cache from the source
#   (a lane made before the shared cache: lanework.sh env LANE > LANE/.git/lane.env)
#   lanework.sh trial <a> <b> [repo]     merge a and b in memory with work's
#                                        merge drivers; lists the conflicts
#   lanework.sh drivers [dir]            write the merge drivers dir's
#                                        .gitattributes names into its clone
#
# The same verbs as the Rift-era lane-rift.sh, over `work`. The source is
# a base `work init` made; a lane is its workspace named <branch>. <base>
# is a branch name or a commit of the main checkout. When <branch> already
# exists in the main checkout, the lane continues it and <base> is only
# checked to be its ancestor. This script never pushes to origin and
# deletes no file but the shared Go cache's oldest entries (trim_gocache):
# removal is `work rm` (the trash; `work gc` empties it, and
# `work gc -idle 48h` collects lanes left idle whose work is held).
set -eu

SRC=${ERGO_WORK_SOURCE:-/workspaces/ergo}
MAIN=${ERGO_MAIN_CHECKOUT:-/home/exedev/Developer/personal/ergo}
WORK=${WORK:-work}
MIN_FREE_GIB=${LANE_MIN_FREE_GIB:-10}
# The Go build cache every lane shares: one folder on the source's file
# system, outside the source and every workspace, so no clone or snapshot
# copies it. Go's cache is safe for concurrent go commands and trims
# itself by age (a day's trim of entries unused for five days), never by
# size: lanes kept 319 GB of distinct entries, all used within five days
# (2026-11-22), so trim_gocache bounds it by size too.
SHARED_CACHE=${LANE_GOCACHE:-/workspaces/.cache/ergo-gocache}
SHARED_CACHE_MAX_GIB=${LANE_GOCACHE_MAX_GIB:-40}

fail() {
  printf 'lanework: %s\n' "$*" >&2
  exit 1
}

# need_base checks what the lane verbs use: the work base, the main
# checkout, work and jq. trial and drivers need only git and the
# checkout's own .gitattributes, on any host.
need_base() {
  [ -n "$SRC" ] && [ -n "$MAIN" ] || fail 'empty source or main checkout path'
  case "$SRC" in /*) ;; *) fail "source is not an absolute path: $SRC" ;; esac
  [ -d "$SRC/.git" ] && [ -f "$SRC/work.toml" ] || fail "not a work base (run work init there): $SRC"
  [ -d "$MAIN/.git" ] || fail "not the main checkout: $MAIN"
  command -v "$WORK" >/dev/null || fail "no $WORK on PATH (ergo build -o ... ./cmd/work in examples/work)"
  command -v jq >/dev/null || fail 'no jq on PATH'
}

check_disk() {
  free_kib=$(df -Pk "$SRC" | awk 'NR==2 {print $4}')
  [ "$free_kib" -ge $((MIN_FREE_GIB * 1024 * 1024)) ] ||
    fail "$SRC's disk has less than ${MIN_FREE_GIB}G free ($((free_kib / 1024 / 1024))G); stop"
}

# The bounded scope common.md's memory rule asks for.
bounded() {
  systemd-run --user --scope -q -p MemoryMax=8G -p MemorySwapMax=0 -- nice -n 19 "$@"
}

# inbase runs the work tool in the source base.
inbase() {
  (cd "$SRC" && "$WORK" "$@")
}

# lane_of prints "name<TAB>id<TAB>path" of the active workspace whose
# checkout holds dir (the working folder when empty).
lane_of() {
  dir=${1:-$PWD}
  top=$(git -C "$dir" rev-parse --show-toplevel 2>/dev/null) || fail "not a Git checkout: $dir"
  found=$(inbase --json list | jq -r --arg p "$top" '.[] | select(.path == $p and .state._tag == "Active") | [.name, .id, .path] | @tsv')
  [ -n "$found" ] || fail "not an active lane of $SRC: $top"
  printf '%s\n' "$found"
}

# lane_gocache prints the shared GOCACHE and makes its folder. It refuses
# a cache that is not absolute, or that lies inside the source, the
# source's workspace storage or the lane at $1 (when given), where a
# clone or a snapshot would copy it.
lane_gocache() {
  case "$SHARED_CACHE" in
    /*) ;;
    *) fail "the shared Go cache is not an absolute path: '$SHARED_CACHE'" ;;
  esac
  case "$SHARED_CACHE/" in
    */./* | */../* | *//*) fail "the shared Go cache has a . or .. or empty part: $SHARED_CACHE" ;;
  esac
  for inside in "$SRC" "${SRC%/*}/.work" "${SRC%/*}/.rifts" ${1:+"$1"}; do
    case "$SHARED_CACHE/" in
      "$inside"/*) fail "the shared Go cache $SHARED_CACHE is inside $inside, which a clone or a snapshot copies" ;;
    esac
  done
  mkdir -p "$SHARED_CACHE"
  printf '%s\n' "$SHARED_CACHE"
}

# trim_gocache bounds the shared cache at $1 by size, on the terms Go's
# own trim deletes on. Over SHARED_CACHE_MAX_GIB, it deletes the entries
# unused the longest, in steps of last use from four days down to two
# hours, until the cache is under the bound. Go marks an entry used at
# most once an hour, so an entry that a running build looked up is never
# deleted. Only the cache's entries (the files in its 00 to ff folders)
# are deleted, and one trim runs at a time; a second one skips.
trim_gocache() {
  case "$1" in /?*) ;; *) fail "trim of a cache that is not an absolute path: '$1'" ;; esac
  limit_kib=$((SHARED_CACHE_MAX_GIB * 1024 * 1024))
  (
    flock -n 8 || exit 0
    for minutes in 5760 2880 1440 720 360 120; do
      [ "$(du -sk "$1" | cut -f1)" -gt "$limit_kib" ] || break
      nice -n 19 find "$1" -mindepth 2 -maxdepth 2 -type f -mmin +"$minutes" -delete
    done
  ) 8>"$1/trim.lock"
}

lane_env() {
  cache=$(lane_gocache "$1")
  printf 'export GOCACHE=%s\n' "$cache"
  printf 'export GOMEMLIMIT=6GiB\n'
  bin=$(lane_go_bin)
  if [ -n "$bin" ]; then
    printf 'export PATH=%s:$PATH GOTOOLCHAIN=local\n' "$bin"
  fi
  # GOROOT stated: a Go built with -trimpath (a test binary, ergo
  # itself) has no GOROOT of its own, and go/build's default then finds
  # no standard library (TRIMPATH-SHARE measure 1).
  printf 'export GOROOT=%s\n' "$(lane_goroot "$bin")"
  printf 'umask 022\n'
}

# lane_goroot prints the GOROOT of the lane's Go: the folder above the
# toolchain's bin, or the go on PATH's when the lane has none.
lane_goroot() {
  if [ -n "$1" ]; then
    printf '%s\n' "${1%/bin}"
    return
  fi
  root=$(go env GOROOT)
  [ -n "$root" ] || fail 'go env GOROOT is empty'
  printf '%s\n' "$root"
}

# lane_go_bin prints the folder of the Go the source's go.mod names on
# its toolchain line, so a lane builds with it first on its PATH: by
# default the module cache's golang.org/toolchain of that version, for
# this host's GOOS and GOARCH; LANE_GO_BIN names another, which must
# hold go. With no toolchain line, or no such toolchain downloaded, it
# prints nothing and the lane uses the go on its PATH.
lane_go_bin() {
  if [ -n "${LANE_GO_BIN:-}" ]; then
    [ -x "$LANE_GO_BIN/go" ] || fail "LANE_GO_BIN has no go: $LANE_GO_BIN"
    printf '%s\n' "$LANE_GO_BIN"
    return
  fi
  version=$(awk '$1 == "toolchain" { print $2 }' "$SRC/go.mod" 2>/dev/null || true)
  [ -n "$version" ] || return 0
  dir="$(go env GOMODCACHE)/golang.org/toolchain@v0.0.1-$version.$(go env GOOS)-$(go env GOARCH)/bin"
  if [ -x "$dir/go" ]; then
    printf '%s\n' "$dir"
  fi
}

lock_source() {
  exec 9>"$(git -C "$SRC" rev-parse --path-format=absolute --git-path worklane.lock)"
  flock 9
}

cmd_warm() {
  check_disk
  cache=$(lane_gocache)
  trim_gocache "$cache"
  (
    cd "$SRC"
    umask 022
    export GOCACHE="$cache" GOMEMLIMIT=6GiB GOMAXPROCS=${GOMAXPROCS:-2} GOFLAGS=-p=2
    bounded go build ./...
    # The ergo tool builds without cgo (internal/ergobuild), and a package
    # above net is a separate cache entry per cgo setting.
    CGO_ENABLED=0 bounded go build -trimpath -buildvcs=false -o /dev/null ./cmd/ergo
    bounded go vet ./...
  ) 9>&-
}

cmd_create() {
  [ "$#" -eq 2 ] || fail 'usage: create <branch> <base>'
  branch=$1
  base=$2
  [ "$branch" != main ] || fail 'a lane never works on main'
  git check-ref-format --branch "$branch" >/dev/null || fail "bad branch name: $branch"
  check_disk
  # A cache a clone would copy is refused before any lane is made.
  cache=$(lane_gocache)
  trim_gocache "$cache"

  base_sha=$(git -C "$MAIN" rev-parse --verify --quiet "$base^{commit}") || fail "base not in the main checkout: $base"
  branch_sha=$(git -C "$MAIN" rev-parse --verify --quiet "refs/heads/$branch^{commit}" || true)

  lock_source
  # Fetch main, the base commit and an existing lane branch from the main checkout.
  git -C "$SRC" fetch --quiet --no-tags main-checkout \
    '+refs/heads/main:refs/remotes/main-checkout/main' "$base_sha" >&2
  start=$base_sha
  if [ -n "$branch_sha" ]; then
    git -C "$SRC" fetch --quiet --no-tags main-checkout "+refs/heads/$branch:refs/remotes/main-checkout/$branch" >&2
    git -C "$SRC" merge-base --is-ancestor "$base_sha" "$branch_sha" ||
      fail "$branch exists in the main checkout and does not contain $base"
    start=$branch_sha
  fi
  # Keep the source near main so a lane's clone starts close to its base.
  if [ -z "$(git -C "$SRC" status --porcelain --untracked-files=no)" ] &&
    git -C "$SRC" merge-base --is-ancestor HEAD refs/remotes/main-checkout/main; then
    git -C "$SRC" merge --quiet --ff-only refs/remotes/main-checkout/main >&2
  fi
  path=$(inbase --json new -branch "$branch" -from "$start" "$branch" | jq -r '.path')
  exec 9>&-
  [ -n "$path" ] && [ "$path" != null ] && [ -d "$path/.git" ] || fail "work new made no lane for $branch"
  [ "$(git -C "$path" symbolic-ref --quiet --short HEAD)" = "$branch" ] || fail "the lane at $path is not on $branch"

  # Lanes never push to origin; only `push` below, into the main checkout.
  # work new guards every remote of the clone, main-checkout too
  # (no-push:workspaces-never-push-origin); the main checkout is the one
  # remote a lane pushes to, so its guard goes.
  git -C "$path" config remote.origin.pushurl 'no-push:lanes-never-push-origin'
  git -C "$path" config --unset-all remote.main-checkout.pushurl || true
  git -C "$path" config remote.pushDefault main-checkout
  git -C "$path" config "branch.$branch.pushRemote" main-checkout
  lane_env "$path" >"$path/.git/lane.env"
  printf '%s\n' "$path"
}

cmd_env() {
  found=$(lane_of "${1:-}")
  lane_env "$(printf '%s\n' "$found" | cut -f3)"
}

cmd_push() {
  found=$(lane_of "${1:-}")
  lane=$(printf '%s\n' "$found" | cut -f3)
  branch=$(git -C "$lane" symbolic-ref --quiet --short HEAD) || fail 'detached HEAD; switch to the lane branch'
  [ "$branch" != main ] || fail 'a lane never pushes main; the orchestrator lands main'
  [ -z "$(git -C "$lane" status --porcelain --untracked-files=no)" ] || fail 'uncommitted changes; commit first'
  # A fast-forward push only (no --force): a rejected push means the main
  # checkout's branch moved; merge it in (git fetch main-checkout B; git merge).
  # It pushes to the main checkout's path, not to a remote: a lane made by
  # an older create, or by work new alone, carries work's push guard on
  # every remote, main-checkout included.
  git -C "$lane" push --quiet "$MAIN" "HEAD:refs/heads/$branch"
  printf 'pushed %s %s into %s\n' "$branch" "$(git -C "$lane" rev-parse HEAD)" "$MAIN"
}

cmd_remove() {
  [ "$#" -ge 1 ] || fail 'usage: remove <lane> [--force]'
  found=$(lane_of "$1")
  name=$(printf '%s\n' "$found" | cut -f1)
  id=$(printf '%s\n' "$found" | cut -f2)
  lane=$(printf '%s\n' "$found" | cut -f3)
  force=${2:-}
  [ -z "$force" ] || [ "$force" = --force ] || fail "unknown flag: $force"
  if [ "$force" != --force ]; then
    [ -z "$(git -C "$lane" status --porcelain)" ] || fail "uncommitted or untracked files in $lane (pass --force to drop them)"
    if branch=$(git -C "$lane" symbolic-ref --quiet --short HEAD); then
      head=$(git -C "$lane" rev-parse HEAD)
      landed=$(git -C "$MAIN" rev-parse --verify --quiet "refs/heads/$branch" || true)
      [ "$landed" = "$head" ] || fail "$branch at $head is not in the main checkout (has ${landed:-nothing}); run push first"
    fi
  fi
  case "$PWD/" in "$lane"/*) fail 'run remove from outside the lane' ;; esac
  # The lane's work is held in the main checkout (checked above), not in
  # the source, so work's own held check cannot see it: -abandon, bound to
  # this lane's id by -expect.
  inbase rm -abandon -expect "$id" "$name" >/dev/null
  printf 'removed %s (in the work trash until the next work gc)\n' "$lane"
}

# merge_drivers prints, once each, the merge drivers the attributes text on
# stdin names (merge=NAME) that Git does not have built in; comments and
# macro lines are skipped, as work reads them (modules/work/work/merge.ergo:
# MergeLines; the gate's first check: scripts/gate/drivers.go).
merge_drivers() {
  awk 'NF >= 2 && $1 !~ /^#/ && $1 !~ /^\[attr\]/ {
    for (i = 2; i <= NF; i++) if ($i ~ /^merge=./) {
      n = substr($i, 7)
      if (n != "text" && n != "binary" && n != "union" && !(n in seen)) { seen[n] = 1; print n }
    }
  }'
}

# driver_setting prints merge.<name>.<key> by the driver table in the
# header of the attributes text on stdin, the lines
#   #   git config merge.ergo-$c.<key> "<value>"
# with $c the driver's class (ergo-<class>); empty when it has none.
driver_setting() {
  awk -v name="$1" -v key="$2" '
    /^#[ \t]*git config merge\.ergo-\$c\./ && substr(name, 1, 5) == "ergo-" {
      rest = $0
      sub(/^#[ \t]*git config merge\.ergo-\$c\./, "", rest)
      k = rest
      sub(/ .*/, "", k)
      if (k != key) next
      v = rest
      sub(/^[^"]*"/, "", v)
      sub(/"[ \t]*$/, "", v)
      gsub(/\$c/, substr(name, 6), v)
      print v
      exit
    }'
}

# attributes prints the .gitattributes of each commit named and of the
# worktree of the clone at <repo>, the first argument.
attributes() {
  repo=$1
  shift
  for c in "$@"; do git -C "$repo" show "$c:.gitattributes" 2>/dev/null || true; done
  cat "$repo/.gitattributes" 2>/dev/null || true
}

# trial merges <a> and <b> of [repo] (the working folder's clone by
# default) in memory with git merge-tree, giving it by -c each driver the
# attributes of a, b and the worktree name, as the clone's configuration
# has it, else by the attributes' driver table: it writes no
# configuration and no ref. It prints each conflicted file and exits 1 on
# any, 0 when the merge is clean.
cmd_trial() {
  [ "$#" -ge 2 ] && [ "$#" -le 3 ] || fail 'usage: trial <a> <b> [repo]'
  repo=$(git -C "${3:-$PWD}" rev-parse --show-toplevel 2>/dev/null) || fail "not a Git checkout: ${3:-$PWD}"
  a=$(git -C "$repo" rev-parse --verify --quiet "$1^{commit}") || fail "no commit $1 in $repo"
  b=$(git -C "$repo" rev-parse --verify --quiet "$2^{commit}") || fail "no commit $2 in $repo"
  left=$1 right=$2
  names=$(attributes "$repo" "$a" "$b" | merge_drivers)
  set --
  for n in $names; do
    driver=$(git -C "$repo" config --get "merge.$n.driver" || attributes "$repo" "$a" "$b" | driver_setting "$n" driver)
    [ -n "$driver" ] || fail "merge driver $n is in neither $repo's configuration nor the attributes' driver table"
    set -- "$@" -c "merge.$n.driver=$driver"
  done
  status=0
  out=$(git -C "$repo" "$@" merge-tree --write-tree --name-only --no-messages "$a" "$b") || status=$?
  case "$status" in
    0) printf 'trial: %s and %s merge clean (tree %s)\n' "$left" "$right" "$out" ;;
    1)
      files=$(printf '%s\n' "$out" | sed 1d | sort -u)
      printf '%s\n' "$files" | sed 's/^/conflict /'
      printf 'trial: %s and %s conflict in %s files\n' "$left" "$right" "$(printf '%s\n' "$files" | wc -l | tr -d ' ')"
      exit 1
      ;;
    *) fail "git merge-tree failed ($status) on $left and $right" ;;
  esac
}

# drivers writes into the clone at [dir] (the working folder's by
# default) each merge driver its .gitattributes names and its
# configuration lacks. work's own table is the source: `work doctor -here
# -fix` registers its drivers in that clone (modules/work, DriverSettings;
# TestDriverTableIsWorks holds the header's table to it). A driver it did
# not write (no work on PATH, a work too old for -here, a driver of no
# class of work's) is written by the driver table in the attributes'
# header. The gate refuses a lane without them (scripts/gate/drivers.go)
# and names this command.
cmd_drivers() {
  dir=$(git -C "${1:-$PWD}" rev-parse --show-toplevel 2>/dev/null) || fail "not a Git checkout: ${1:-$PWD}"
  [ -f "$dir/.gitattributes" ] || return 0
  if command -v "$WORK" >/dev/null 2>&1; then
    (cd "$dir" && "$WORK" doctor -here -fix >/dev/null 2>&1) && printf 'work doctor -here -fix in %s\n' "$dir" || true
  fi
  for n in $(merge_drivers <"$dir/.gitattributes"); do
    git -C "$dir" config --get "merge.$n.driver" >/dev/null && continue
    for k in name driver; do
      value=$(driver_setting "$n" "$k" <"$dir/.gitattributes")
      [ -n "$value" ] || fail "no merge.$n.$k in the driver table of $dir/.gitattributes"
      git -C "$dir" config "merge.$n.$k" "$value"
      printf 'set merge.%s.%s in %s\n' "$n" "$k" "$dir"
    done
  done
}

command=${1:-}
[ "$#" -gt 0 ] && shift
case "$command" in
  create) need_base; cmd_create "$@" ;;
  env) need_base; cmd_env "$@" ;;
  push) need_base; cmd_push "$@" ;;
  remove) need_base; cmd_remove "$@" ;;
  warm) need_base; cmd_warm ;;
  trial) cmd_trial "$@" ;;
  drivers) cmd_drivers "$@" ;;
  *) sed -n '2,14p' "$0" >&2; exit 2 ;;

esac
