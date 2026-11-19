#!/bin/sh
# workcost.sh TREE: the counted receipt of docs/receipts/work-vs-rift.md
# for one source tree (SEMWS WG0). It counts create, remove and purge, and
# the first git status in a new workspace, for Rift's npm wrapper, Rift's
# native binary, work, and work through the npm package's bin: user-mode
# instructions (cnt), system calls (strace -c) and exclusive bytes
# (btrfs filesystem du). Every run is bounded (a user scope with 8G and
# no swap, nice 19). It writes the rows on standard output.
#
# Environment (all required but CAP):
#   S       a scratch folder on btrfs; S/src/TREE is the source, a btrfs
#           subvolume holding a Git checkout
#   CNT     the cnt binary (go build ./scripts/workcost/cnt)
#   WORK    the work binary; NPMWORK the bin of an installed @cevr/work
#   RIFT    Rift's npm wrapper (rift); RN Rift's native binary
#   CAP     the most 30-second waits for a 1-minute load under 48 (20)
#
# Rift and work each keep their registry in S (XDG_DATA_HOME), so neither
# reads or removes anything outside it.
set -u
T=$1
: "${S:?}" "${CNT:?}" "${WORK:?}" "${NPMWORK:?}" "${RIFT:?}" "${RN:?}"
SRC=$S/src/$T
D=$S/rifts/$T
ST=$S/strace/$T
export XDG_DATA_HOME=$S/xdg TMPDIR=$S/tmp HOME=$S/home WORK_BASE=$SRC
mkdir -p "$D" "$ST" "$TMPDIR" "$HOME" "$XDG_DATA_HOME"
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
	"$CNT" -l "$l" -- systemd-run --user --scope -q -p MemoryMax=8G -p MemorySwapMax=0 -- nice -n 19 "$@" 2>/dev/null
}
s() {
	l=$1
	shift
	b strace -f -c -S calls -o "$ST/$l.txt" "$@" >/dev/null 2>&1
	echo "$l	syscalls=$(awk '$NF=="total"{print $(NF-2)}' "$ST/$l.txt")	read=$(awk '$NF=="read"{print $(NF-1)}' "$ST/$l.txt")	openat=$(awk '$NF=="openat"{print $(NF-1)}' "$ST/$l.txt")	newfstatat=$(awk '$NF=="newfstatat"{print $(NF-1)}' "$ST/$l.txt")	top: $(awk 'NR>2 && $NF!="total" && NF>=5 {print $NF"="$4}' "$ST/$l.txt" | head -6 | tr '\n' ' ')"
}
du1() {
	sync
	for p in "$@"; do
		sudo -n btrfs filesystem du -s --raw "$p" 2>/dev/null | awk 'NR>1{printf "%s\ttotal=%d\texclusive=%d\tset_shared=%d\n",$4,$1,$2,$3}'
	done
}
home=$(dirname "$SRC")/.work/$T
echo "# work vs Rift, $T; load at start: $(cat /proc/loadavg) (waited $n x 30 s); $(date -u +%FT%TZ)"
echo "# source: $SRC files=$(find "$SRC" -xdev -type f | wc -l) tracked=$(git -C "$SRC" ls-files | wc -l) head=$(git -C "$SRC" rev-parse --short HEAD)"
echo "# work: $("$WORK" --help 2>&1 | head -1 | cut -c1-60); npm bin: $NPMWORK -> $(readlink -f "$NPMWORK")"
"$RN" init "$SRC" >/dev/null 2>&1
grep -qx work.toml "$SRC/.git/info/exclude" 2>/dev/null || echo work.toml >>"$SRC/.git/info/exclude"
"$WORK" init >/dev/null
git -C "$SRC" update-index -q --refresh
echo "## baseline: the bounded scope alone (subtract from each cnt row)"
c baseline-scope-true /bin/true
c baseline-scope-true /bin/true
echo "## create (a full copy: Rift's --copy-all, work's default)"
c rift-wrapper-create "$RIFT" create --copy-all --no-hooks --name a1 --into "$D" "$SRC"
c rift-native-create "$RN" create --copy-all --no-hooks --name a2 --into "$D" "$SRC"
s rift-native-create "$RN" create --copy-all --no-hooks --name a3 --into "$D" "$SRC"
c work-create "$WORK" new w1
c work-npm-create "$NPMWORK" new w2
c work-create "$WORK" new w3
c work-npm-create "$NPMWORK" new w4
s work-create "$WORK" new w5
echo "## bytes after create (btrfs filesystem du -s --raw)"
du1 "$D/a2" "$home/w1"
echo "cloner of w1: $("$WORK" --json list | tr '{' '\n' | grep '"name":"w1"' | sed 's/.*"cloner":"\([^"]*\)".*/\1/')"
echo "## the first git status in the new workspace"
c rift-copy-first-status git -C "$D/a2" status --porcelain
c work-first-status git -C "$home/w1" status --porcelain
s rift-copy-first-status git -C "$D/a3" status --porcelain
s work-first-status git -C "$home/w5" status --porcelain
echo "status lines: rift=$(git -C "$D/a2" status --porcelain | wc -l) work=$(git -C "$home/w1" status --porcelain | wc -l)"
echo "## remove (to the trash)"
c rift-wrapper-remove "$RIFT" remove --no-hooks "$D/a1"
c rift-native-remove "$RN" remove --no-hooks "$D/a2"
s rift-native-remove "$RN" remove --no-hooks "$D/a3"
c work-remove "$WORK" rm w1
c work-npm-remove "$NPMWORK" rm w2
s work-remove "$WORK" rm w5
echo "## purge of the trash"
c rift-native-gc-3-entries "$RN" gc
c work-gc-3-entries "$WORK" gc
"$WORK" rm w3 >/dev/null 2>&1
"$WORK" rm w4 >/dev/null 2>&1
s work-gc-2-entries "$WORK" gc
echo "## end; load: $(cat /proc/loadavg)"
