#!/bin/sh
# workrace.sh N: the race receipt of docs/receipts/work-race.md (SEMWS
# WG1). N trials (300 by default) of a remove racing a create on top of
# the workspace it removes, two work processes at once: in trial i, work
# new a<i>, then at the same time `work rm -abandon -expect ID a<i>` and
# `work new -on a<i> b<i>`, each after a random pause under 120 ms. After
# both end it counts the state: each call's status, and for a<i> and b<i>
# whether the folder is there and what the registry says. At the end it
# counts the folders under the storage that no record names (orphans) and
# the records whose folder is missing. Rift's numbers for the same race
# are in the receipt (Rift 0.0.13, the SEMWS r2 research run).
#
# Environment:
#   S     a scratch folder on btrfs that the script owns (required; must
#         be under /workspaces/.scratch/ or /tmp/opencode/); S/root is the
#         base, made once
#   WORK  the work binary (required)
#   CAP   the most 30-second waits for a 1-minute load under 48 (90)
#
# work keeps its storage in S (HOME and XDG_DATA_HOME are S), so the run
# reads and writes nothing outside S. Every work call runs in a user scope
# with 8G and no swap, nice 19. The rows go to standard output.
set -eu
N=${1:-300}
: "${S:?}" "${WORK:?}"
case "$S" in
/workspaces/.scratch/?*|/tmp/opencode/?*) ;;
*) echo "workrace: S=$S is not under /workspaces/.scratch/ or /tmp/opencode/" >&2; exit 2 ;;
esac
case "$N" in ''|*[!0-9]*) echo "workrace: N=$N is not a number" >&2; exit 2 ;; esac
export HOME="$S/home" XDG_DATA_HOME="$S/xdg" TMPDIR="$S/tmp" WORK_BASE="$S/root"
export GIT_AUTHOR_NAME=race GIT_AUTHOR_EMAIL=race@ergo.invalid GIT_COMMITTER_NAME=race GIT_COMMITTER_EMAIL=race@ergo.invalid
mkdir -p "$HOME" "$XDG_DATA_HOME" "$TMPDIR"
waits=0
until [ "$(cut -d. -f1 /proc/loadavg)" -lt 48 ]; do
	sleep 30
	waits=$((waits + 1))
	[ $waits -gt "${CAP:-90}" ] && break
done
w() { systemd-run --user --scope -q -p MemoryMax=8G -p MemorySwapMax=0 -- nice -n 19 "$WORK" "$@"; }
if [ ! -d "$S/root" ]; then
	btrfs subvolume create "$S/root" > /dev/null
	(cd "$S/root" && git init -q -b main . && for i in 1 2 3 4 5; do echo "file $i" > "f$i.txt"; done && git add -A && git commit -qm init)
fi
cd "$S/root"
w init > /dev/null 2>&1 || true
H=$S/.work/root
R=$H/.registry
[ -f "$R" ] || { echo "workrace: no registry at $R" >&2; exit 2; }
# state NAME: the registry's state of NAME (none when it has no record):
# the last put of the name wins, a drop or a tomb forgets it.
state() {
	awk -F'\t' -v n="\"$1\"" '$2 == n { if ($1 == "put") s = $7; else s = "none" } END { print (s == "" ? "none" : s) }' "$R"
}
pause() { awk -v s="$(od -An -N2 -tu2 /dev/urandom)" 'BEGIN { printf "0.%03d", s % 120 }'; }
echo "# workrace: remove racing a create on it, $N trials; load at start: $(cat /proc/loadavg) (waited $waits x 30 s); $(date -u +%FT%TZ)"
echo "# work: $WORK"
out=$S/tmp/rows
: > "$out"
i=1
while [ "$i" -le "$N" ]; do
	w new "a$i" > /dev/null 2>&1
	id=$(cat "$H/a$i/.git/work-id")
	d1=$(pause)
	d2=$(pause)
	(sleep "$d1"; rc=0; w rm -abandon -expect "$id" "a$i" > /dev/null 2> "$S/tmp/rm.err" || rc=$?; echo $rc > "$S/tmp/rm.rc") &
	(sleep "$d2"; rc=0; w new -on "a$i" "b$i" > /dev/null 2> "$S/tmp/new.err" || rc=$?; echo $rc > "$S/tmp/new.rc") &
	wait
	da=0; [ -d "$H/a$i" ] && da=1
	db=0; [ -d "$H/b$i" ] && db=1
	why=$(sed -n 's/^work: \([A-Za-z]*\):.*/\1/p' "$S/tmp/rm.err" | head -1)
	nwhy=$(sed -n 's/^work: \([A-Za-z]*\):.*/\1/p' "$S/tmp/new.err" | head -1)
	echo "rm=$(cat "$S/tmp/rm.rc")${why:+ $why} new=$(cat "$S/tmp/new.rc")${nwhy:+ $nwhy} | a: dir=$da row=$(state "a$i") | b: dir=$db row=$(state "b$i")" >> "$out"
	i=$((i + 1))
done
echo "## work outcomes (count: state after both calls ended)"
sort "$out" | uniq -c | sort -rn
# Every folder the records name (live ones and the trash), and every
# folder under the storage (its workspaces and its trash).
named=$S/tmp/named
awk -F'\t' '$1 == "put" { p[$2] = $3 } $1 == "drop" || $1 == "tomb" { delete p[$2] } END { for (k in p) { v = p[k]; gsub(/"/, "", v); print v } }' "$R" | sort > "$named"
there=$S/tmp/there
{ find "$H" -mindepth 1 -maxdepth 1 -type d ! -name '.*'; find "$H/.work-trash" -mindepth 1 -maxdepth 1 -type d 2> /dev/null || true; } | sort > "$there"
echo "orphans (a folder no record names): $(comm -13 "$named" "$there" | wc -l)"
echo "records whose folder is missing: $(comm -23 "$named" "$there" | wc -l)"
echo "## end; load: $(cat /proc/loadavg)"
