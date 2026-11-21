#!/bin/sh
# snapcost.sh REPO TAG MODE: one snapshot of REPO's working files by the
# git commands of modules/work/git's Snapshot, into the index
# .git/snapcost-TAG (never the repository's own index), and the tree it
# writes. MODE plain starts as Snapshot did before SEMWS WG4b (read-tree
# HEAD: every file hashed), m as it does now (read-tree -m HEAD: a file
# unchanged since the last snapshot into that index keeps its stat data).
# Count it with cnt: `cnt -l m -- sh snapcost.sh REPO x m`, twice.
set -eu
repo=${1:?usage: snapcost.sh REPO TAG plain|m}
tag=${2:?usage: snapcost.sh REPO TAG plain|m}
mode=${3:?usage: snapcost.sh REPO TAG plain|m}
[ -d "$repo/.git" ] || { echo "$repo is no Git checkout" >&2; exit 2; }
case "$tag" in *[!a-z0-9]*|"") echo "TAG: letters and digits" >&2; exit 2 ;; esac
export GIT_INDEX_FILE="$repo/.git/snapcost-$tag"
case "$mode" in
m) git -C "$repo" read-tree -m HEAD ;;
plain) git -C "$repo" read-tree HEAD ;;
*) echo "MODE: plain or m" >&2; exit 2 ;;
esac
git -C "$repo" add --all -- .
git -C "$repo" write-tree
