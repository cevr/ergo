#!/bin/sh
# lockreplay.sh REPO: the lock-file receipt of docs/receipts/work-locks.md
# (SEMWS WG2; the plan's lockprobe.py with work in place of its keyed
# lines). On a Bun repository's history it replays the merges whose two
# sides both changed bun.lock, as Git merges them and as work does:
#
#   Part 1, the real two-parent merges (base: the merge base; ours and
#   theirs: the parents; the committed merge to compare with);
#   Part 2, consecutive first-parent changes of bun.lock as if made in
#   parallel (base: c1, ours: c1^, theirs: c2, where the lock at c2^ is
#   c1's: c2's change applied to the tree without c1's).
#
# Git merges bun.lock by lines; work never does: the lock is derived
# (`bun install --lockfile-only` after the manifests merge), so its
# conflicts are those of the package.json files both sides changed, each
# merged by `work merge-file json` (key paths). For each case it counts
# Git's conflict on the lock and on those manifests, work's on the
# manifests, and in part 1 whether each clean merge of a manifest is the
# committed file. It does not run the package manager (no network): that
# the derived lock is the merged manifests' is memory's
# LockIsTheManifests.
#
# Environment:
#   S     a scratch folder the script owns (required; under
#         /workspaces/.scratch/ or /tmp/opencode/)
#   WORK  the work binary (required)
#   CAP   the most 30-second waits for a 1-minute load under 48 (20)
#
# It writes the rows on standard output and nothing into REPO.
set -eu
R=$1
: "${S:?}" "${WORK:?}"
case $S in
/workspaces/.scratch/?* | /tmp/opencode/?*) ;;
*)
	echo "lockreplay: S must be under /workspaces/.scratch/ or /tmp/opencode/" >&2
	exit 2
	;;
esac
[ -d "$R/.git" ] || {
	echo "lockreplay: $R is not a checkout" >&2
	exit 2
}
mkdir -p "$S"
n=0
until [ "$(cut -d. -f1 /proc/loadavg)" -lt 48 ]; do
	sleep 30
	n=$((n + 1))
	[ $n -gt "${CAP:-20}" ] && break
done
g() { git -C "$R" "$@"; }
# blob REV PATH FILE: the file at REV, or an empty file and status 1.
blob() { g cat-file -p "$1:$2" > "$3" 2> /dev/null || {
	: > "$3"
	return 1
}; }
# manifests B O T: the package.json paths both B..O and B..T changed.
manifests() {
	g diff --name-only "$1" "$2" -- '*package.json' | sort > "$S/m1"
	g diff --name-only "$1" "$3" -- '*package.json' | sort > "$S/m2"
	comm -12 "$S/m1" "$S/m2"
}
# replay ID B O T [M]: one row for a case (M is the committed merge,
# when there is one); nothing when the two sides did not both change the
# lock apart.
replay() {
	id=$1 b=$2 o=$3 t=$4 m=${5:-}
	blob "$b" bun.lock "$S/lb" || true
	blob "$o" bun.lock "$S/lo" || true
	blob "$t" bun.lock "$S/lt" || true
	if cmp -s "$S/lo" "$S/lb" || cmp -s "$S/lt" "$S/lb" || cmp -s "$S/lo" "$S/lt"; then
		return 0
	fi
	if git merge-file -q -p "$S/lo" "$S/lb" "$S/lt" > /dev/null 2>&1; then lock=0; else lock=1; fi
	gm=0 wm=0 nm=0 eq=0 ne=0
	for p in $(manifests "$b" "$o" "$t"); do
		blob "$b" "$p" "$S/b.json" || true
		blob "$o" "$p" "$S/o.json" || continue
		blob "$t" "$p" "$S/t.json" || continue
		nm=$((nm + 1))
		git merge-file -q -p "$S/o.json" "$S/b.json" "$S/t.json" > /dev/null 2>&1 || gm=$((gm + 1))
		if (cd "$S" && WORK_BASE="$S" "$WORK" merge-file -path "$p" json b.json o.json t.json > /dev/null 2>&1); then
			if [ -n "$m" ]; then
				blob "$m" "$p" "$S/r.json" || true
				if cmp -s "$S/o.json" "$S/r.json"; then eq=$((eq + 1)); else ne=$((ne + 1)); fi
			fi
		else
			wm=$((wm + 1))
		fi
	done
	echo "  $id git_lock_conflict=$lock manifests_both_changed=$nm git_conflicts=$gm work_conflicts=$wm work_clean_equal_to_committed=$eq work_clean_different=$ne"
}
# total COMMITTED: the sums of the rows on standard input, then the rows.
total() {
	tee "$S/rows" | awk -v committed="$1" '
	{ n++; for (i = 2; i <= NF; i++) { split($i, kv, "="); s[kv[1]] += kv[2] }
	  if ($0 ~ / git_lock_conflict=1/ || $0 ~ / git_conflicts=[1-9]/) gc++
	  if ($0 ~ / work_conflicts=[1-9]/) wc++ }
	END {
		printf "cases=%d git_conflicts_on_bun.lock=%d cases_git_leaves_conflicted=%d cases_work_leaves_conflicted=%d\n", n, s["git_lock_conflict"], gc, wc
		printf "manifests_both_changed=%d git_line_conflicts=%d work_json_conflicts=%d", s["manifests_both_changed"], s["git_conflicts"], s["work_conflicts"]
		if (committed) printf " work_clean_equal_to_committed=%d work_clean_different=%d", s["work_clean_equal_to_committed"], s["work_clean_different"]
		printf "\n"
	}'
	cat "$S/rows"
}
echo "## Part 1: real two-parent merges"
g rev-list --merges HEAD | while read -r c; do
	mb=$(g merge-base "$c^1" "$c^2" 2> /dev/null) || continue
	replay "$(echo "$c" | cut -c1-9)" "$mb" "$c^1" "$c^2" "$c"
done | total 1
echo "## Part 2: consecutive lock changes as if made in parallel"
g log --first-parent --format=%H -- bun.lock | tac | {
	prev=""
	while read -r c2; do
		if [ -n "$prev" ] && g diff --quiet "$prev" "$c2^" -- bun.lock 2> /dev/null; then
			replay "$(echo "$prev" | cut -c1-7)+$(echo "$c2" | cut -c1-7)" "$prev" "$prev^" "$c2"
		fi
		prev=$c2
	done
} | total ""
