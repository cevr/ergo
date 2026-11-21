#!/bin/sh
# lanework.sh: Ergo lanes in copy-on-write workspaces of `work` (ergo work).
#
#   lanework.sh create <branch> <base>   make a lane workspace for <branch>; prints its path
#   lanework.sh env [lane]               print the lane's env (GOCACHE inside the lane)
#   lanework.sh push [lane]              hand the branch back to the main checkout
#   lanework.sh remove <lane> [--force]  move the lane to the work trash (at READY)
#   lanework.sh warm                     rebuild the warm source's Go cache
#
# The same verbs as the Rift-era lane-rift.sh, over `work`. The source is
# a base `work init` made; a lane is its workspace named <branch>. <base>
# is a branch name or a commit of the main checkout. When <branch> already
# exists in the main checkout, the lane continues it and <base> is only
# checked to be its ancestor. This script never pushes to origin and never
# deletes files itself: removal is `work rm` (the trash; `work gc` empties
# it, and `work gc -idle 48h` collects lanes left idle whose work is held).
set -eu

SRC=${ERGO_WORK_SOURCE:-/workspaces/ergo}
MAIN=${ERGO_MAIN_CHECKOUT:-/home/exedev/Developer/personal/ergo}
WORK=${WORK:-work}
MIN_FREE_GIB=${LANE_MIN_FREE_GIB:-10}

fail() {
  printf 'lanework: %s\n' "$*" >&2
  exit 1
}

[ -n "$SRC" ] && [ -n "$MAIN" ] || fail 'empty source or main checkout path'
case "$SRC" in /*) ;; *) fail "source is not an absolute path: $SRC" ;; esac
[ -d "$SRC/.git" ] && [ -f "$SRC/work.toml" ] || fail "not a work base (run work init there): $SRC"
[ -d "$MAIN/.git" ] || fail "not the main checkout: $MAIN"
command -v "$WORK" >/dev/null || fail "no $WORK on PATH (ergo build -o ... ./cmd/work in examples/work)"
command -v jq >/dev/null || fail 'no jq on PATH'

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

lane_env() {
  printf 'export GOCACHE=%s/.git/gocache\n' "$1"
  printf 'export GOMEMLIMIT=6GiB\n'
  printf 'umask 022\n'
}

lock_source() {
  exec 9>"$(git -C "$SRC" rev-parse --path-format=absolute --git-path worklane.lock)"
  flock 9
}

cmd_warm() {
  check_disk
  (
    cd "$SRC"
    umask 022
    export GOCACHE="$SRC/.git/gocache" GOMEMLIMIT=6GiB GOMAXPROCS=${GOMAXPROCS:-2} GOFLAGS=-p=2
    bounded go build ./...
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

command=${1:-}
[ "$#" -gt 0 ] && shift
case "$command" in
  create) cmd_create "$@" ;;
  env) cmd_env "$@" ;;
  push) cmd_push "$@" ;;
  remove) cmd_remove "$@" ;;
  warm) cmd_warm ;;
  *) sed -n '2,9p' "$0" >&2; exit 2 ;;
esac
