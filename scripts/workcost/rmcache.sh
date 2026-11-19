#!/bin/sh
# rmcache.sh TREE: the rm cost receipt of docs/receipts/work.md, before
# and after the untracked cache (SEMWS WG6a). work new turns on
# core.untrackedCache in each workspace; this counts `work rm` of a
# workspace after one git status (as a lane runs it), with the cache
# turned off again ("before") and left on ("after"), two of each, and the
# held check's own git status the same two ways. Each row: user-mode
# instructions (cnt) and system calls (strace -c). The source is the one
# workcost.sh uses (S/src/TREE, a work base already).
#
# Environment (all required but CAP):
#   S     the workcost scratch folder (under /workspaces/.scratch/ or
#         /tmp/opencode/)
#   CNT   the cnt binary (go build ./scripts/workcost/cnt)
#   WORK  the work binary
#   CAP   the most 30-second waits for a 1-minute load under 48 (20)
set -eu
T=$1
: "${S:?}" "${CNT:?}" "${WORK:?}"
case $S in
/workspaces/.scratch/?* | /tmp/opencode/?*) ;;
*)
	echo "rmcache: S must be under /workspaces/.scratch/ or /tmp/opencode/" >&2
	exit 2
	;;
esac
SRC=$S/src/$T
ST=$S/strace/rmcache-$T
[ -d "$SRC/.git" ] || {
	echo "rmcache: $SRC is not a checkout" >&2
	exit 2
}
export XDG_DATA_HOME=$S/xdg TMPDIR=$S/tmp HOME=$S/home WORK_BASE=$SRC
mkdir -p "$ST" "$TMPDIR" "$HOME"
n=0
until [ "$(cut -d. -f1 /proc/loadavg)" -lt 48 ]; do
	sleep 30
	n=$((n + 1))
	[ $n -gt "${CAP:-20}" ] && break
done
b() { systemd-run --user --scope -q -p MemoryMax=8G -p MemorySwapMax=0 -- nice -n 19 "$@"; }
c() {
	l=$1
	shift
	"$CNT" -l "$l" -- systemd-run --user --scope -q -p MemoryMax=8G -p MemorySwapMax=0 -- nice -n 19 "$@" 2> /dev/null | grep -v '^[ ?MAD]' || true
}
s() {
	l=$1
	shift
	b strace -f -c -S calls -o "$ST/$l.txt" "$@" > /dev/null 2>&1 || true
	echo "$l	syscalls=$(awk '$NF=="total"{print $(NF-2)}' "$ST/$l.txt")"
}
home=$(dirname "$SRC")/.work/$T
echo "# rm before and after the untracked cache, $T; load at start: $(cat /proc/loadavg) (waited $n x 30 s); $(date -u +%FT%TZ)"
c baseline-scope-true /bin/true
for mode in before after; do
	for i in 1 2 3; do
		w=uc-$mode-$i
		b "$WORK" new "$w" > /dev/null
		if [ $mode = before ]; then git -C "$home/$w" config core.untrackedCache false; fi
		git -C "$home/$w" status --porcelain > /dev/null
	done
	echo "## $mode (core.untrackedCache=$(git -C "$home/uc-$mode-1" config core.untrackedCache))"
	c "$mode-held-status" git -C "$home/uc-$mode-1" --no-optional-locks status --porcelain --untracked-files=normal
	c "$mode-rm" "$WORK" rm "uc-$mode-1"
	c "$mode-rm" "$WORK" rm "uc-$mode-2"
	s "$mode-rm" "$WORK" rm "uc-$mode-3"
done
b "$WORK" gc > /dev/null
echo "## end; load: $(cat /proc/loadavg)"
