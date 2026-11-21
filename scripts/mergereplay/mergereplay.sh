#!/bin/sh
# mergereplay.sh REPO: the TypeScript merge receipt of
# docs/receipts/work-typescript.md (SEMWS WG4). On a repository's history
# it replays the real two-parent merges whose two sides both changed a
# .ts or .tsx file (base: the merge base; ours and theirs: the parents;
# the committed merge to compare with), each file merged twice: by
# Git's line merge (git merge-file) and by `work merge-file auto` with a
# trusted [language.typescript] (plugins/typescript, class typescript).
# For each case it records whether each merge conflicts and whether a
# clean merge is the committed file: byte for byte (yes), or with the same
# declarations in another order (order: plugins/typescript/same.js), or
# not (no).
#
#   S        a scratch folder that does not exist yet (under /tmp/opencode
#            or /workspaces/.scratch)
#   WORK     the work binary
#   PLUGIN   plugins/typescript/main.js (default: this checkout's)
#   CNT      optional: the cnt binary (go build ./scripts/workcost/cnt),
#            to count the instructions of each side's merges
#   MAX      optional: the most merges read (default: all)
#
# work merges each case twice: a file at a time (merge-file, as Git's
# driver runs it), and each merge's files at once (merge-folders over the
# three trees of its cases), which starts the plugin twice a merge. The
# two must agree; the totals say how many cases do not.
#
# It reads REPO with git show and writes only under S. The rows go to
# S/rows.txt (merge, path, git's conflicts, work's status, git's clean
# result is the committed file, work's is); the totals to stdout.
set -eu

# mergereplay.sh --loop git|work|tree: one side's merges of every case in
# S/cases.txt (what CNT counts), its statuses in S/git.txt or S/work.txt;
# tree: a merge-folders of each merge's trees, its answer in
# S/trees/MERGE/answer.
if [ "${1:-}" = --loop ]; then
	: "${S:?}"
	if [ "$2" = tree ]; then
		for m in $(awk '{ print $2 }' "$S/cases.txt" | uniq); do
			x="$S/trees/$m"
			WORK_BASE="$S/base" "$WORK" merge-folders "$x/base" "$x/ours" "$x/theirs" "$x/out" > "$x/answer" 2>&1 || true
		done
		exit 0
	fi
	while read -r k m f ext; do
		d="$S/cases/$k"
		if [ "$2" = git ]; then
			git merge-file -p "$d/ours.$ext" "$d/base.$ext" "$d/theirs.$ext" > "$d/git.$ext" 2>/dev/null && st=0 || st=$?
		else
			cp "$d/ours.$ext" "$d/work.$ext"
			WORK_BASE="$S/base" "$WORK" merge-file -path "$f" auto "$d/base.$ext" "$d/work.$ext" "$d/theirs.$ext" > "$d/work.out" 2>&1 && st=0 || st=$?
		fi
		echo "$k $st"
	done < "$S/cases.txt" > "$S/$2.txt"
	exit 0
fi

REPO=${1:?usage: S=... WORK=... mergereplay.sh REPO}
: "${S:?S: a scratch folder}"
: "${WORK:?WORK: the work binary}"
HERE=$(cd "$(dirname "$0")" && pwd)
PLUGIN=${PLUGIN:-$HERE/../../plugins/typescript/main.js}
MAX=${MAX:-0}
case "$S" in /tmp/opencode/?*|/workspaces/.scratch/?*) ;; *) echo "S must be under /tmp/opencode or /workspaces/.scratch" >&2; exit 2 ;; esac
[ ! -e "$S" ] || { echo "$S exists" >&2; exit 2; }
[ -d "$REPO/.git" ] || [ -f "$REPO/HEAD" ] || { echo "$REPO is no Git repository" >&2; exit 2; }
mkdir -p "$S/base" "$S/cases" "$S/home" "$S/xdg"
# work's data (the list of bases) stays in S, not in the user's home.
export HOME="$S/home" XDG_DATA_HOME="$S/xdg"

# The work base: a repository whose work.toml names the plugin, trusted.
(
	cd "$S/base"
	git init -q -b main .
	printf '[language.typescript]\ncommand = "bun %s"\nfiles = ["*.ts", "*.tsx", "*.mts", "*.cts"]\n' "$PLUGIN" > work.toml
	git add work.toml
	git -c user.email=r@ergo.invalid -c user.name=r commit -q -m base
	WORK_BASE="$S/base" "$WORK" init >/dev/null
	WORK_BASE="$S/base" "$WORK" trust >/dev/null
)

# The cases: each file both parents changed from the merge base, that the
# base, both parents and the merge all hold.
n=0
: > "$S/cases.txt"
for m in $(git -C "$REPO" rev-list --merges HEAD); do
	if [ "$MAX" -gt 0 ] && [ "$n" -ge "$MAX" ]; then break; fi
	n=$((n + 1))
	b=$(git -C "$REPO" merge-base "$m^1" "$m^2" 2>/dev/null) || continue
	git -C "$REPO" diff --no-renames --name-only "$b" "$m^1" -- '*.ts' '*.tsx' | sort > "$S/one"
	git -C "$REPO" diff --no-renames --name-only "$b" "$m^2" -- '*.ts' '*.tsx' | sort > "$S/two"
	for f in $(comm -12 "$S/one" "$S/two"); do
		k=$(wc -l < "$S/cases.txt" | tr -d ' ')
		d="$S/cases/$k"
		mkdir -p "$d"
		ext=${f##*.}
		git -C "$REPO" show "$b:$f" > "$d/base.$ext" 2>/dev/null || continue
		git -C "$REPO" show "$m^1:$f" > "$d/ours.$ext" 2>/dev/null || continue
		git -C "$REPO" show "$m^2:$f" > "$d/theirs.$ext" 2>/dev/null || continue
		git -C "$REPO" show "$m:$f" > "$d/merged.$ext" 2>/dev/null || continue
		printf '%s %s %s %s\n' "$k" "$m" "$f" "$ext" >> "$S/cases.txt"
		for side in base ours theirs; do
			mkdir -p "$(dirname "$S/trees/$m/$side/$f")"
			cp "$d/$side.$ext" "$S/trees/$m/$side/$f"
		done
	done
done

# Git's merges, then work's, each in one loop (counted when CNT is set).
export S WORK
if [ -n "${CNT:-}" ]; then
	"$CNT" -l git -- sh "$HERE/mergereplay.sh" --loop git | tail -1
	"$CNT" -l work -- sh "$HERE/mergereplay.sh" --loop work | tail -1
	"$CNT" -l work-folders -- sh "$HERE/mergereplay.sh" --loop tree | tail -1
else
	sh "$HERE/mergereplay.sh" --loop git
	sh "$HERE/mergereplay.sh" --loop work
	sh "$HERE/mergereplay.sh" --loop tree
fi

# merge-folders against merge-file: the same text, and a conflict in its
# answer exactly when merge-file conflicted.
apart=0
while read -r k m f ext; do
	d="$S/cases/$k"
	w=$(awk -v k="$k" '$1 == k { print $2 }' "$S/work.txt")
	c=0
	grep -F "\"$f\"" "$S/trees/$m/answer" >/dev/null && c=1
	if ! cmp -s "$S/trees/$m/out/$f" "$d/work.$ext" || { [ "$w" = 0 ] && [ "$c" = 1 ]; } || { [ "$w" != 0 ] && [ "$c" = 0 ]; }; then
		apart=$((apart + 1))
		echo "$k $m $f" >> "$S/apart.txt"
	fi
done < "$S/cases.txt"
echo "merge-folders and merge-file apart: $apart"

# The rows and the totals.
: > "$S/rows.txt"
while read -r k m f ext; do
	d="$S/cases/$k"
	g=$(awk -v k="$k" '$1 == k { print $2 }' "$S/git.txt")
	w=$(awk -v k="$k" '$1 == k { print $2 }' "$S/work.txt")
	ge=no; we=no
	cmp -s "$d/git.$ext" "$d/merged.$ext" && ge=yes
	cmp -s "$d/work.$ext" "$d/merged.$ext" && we=yes
	if [ "$ge" = no ] && bun "$(dirname "$PLUGIN")/same.js" "$d/git.$ext" "$d/merged.$ext" 2>/dev/null; then ge=order; fi
	if [ "$we" = no ] && bun "$(dirname "$PLUGIN")/same.js" "$d/work.$ext" "$d/merged.$ext" 2>/dev/null; then we=order; fi
	printf '%s %s %s git=%s work=%s git-is-merge=%s work-is-merge=%s\n' "$k" "$m" "$f" "$g" "$w" "$ge" "$we" >> "$S/rows.txt"
done < "$S/cases.txt"
awk '
{ split($4, g, "="); split($5, w, "="); split($6, ge, "="); split($7, we, "=")
  cases++
  gc = g[2] != 0; wc = w[2] != 0
  if (gc) gitc++; if (wc) workc++
  if (gc && !wc) { saved++; if (we[2] == "yes") savedEq++; if (we[2] == "order") savedOrder++ }
  if (!gc && wc) lost++
  if (!gc && ge[2] == "yes") gitEq++
  if (!wc && we[2] == "yes") workEq++
  if (!gc && ge[2] == "order") gitOrder++
  if (!wc && we[2] == "order") workOrder++
  if (!gc && ge[2] == "yes" && !wc && we[2] == "order") moved++
  if (!gc && ge[2] == "yes" && !wc && we[2] == "no") worse++
  if (!gc && ge[2] != "yes" && !wc && we[2] == "yes") better++ }
END {
  printf "cases (a .ts or .tsx file both sides of a merge changed): %d\n", cases
  printf "conflicted: by git %d, by work %d\n", gitc, workc
  printf "git conflicted, work clean: %d (the committed file: %d; its declarations in another order: %d)\n", saved, savedEq, savedOrder
  printf "git clean, work conflicted: %d\n", lost
  printf "clean and the committed file: by git %d, by work %d\n", gitEq, workEq
  printf "clean and the committed declarations in another order: by git %d, by work %d\n", gitOrder, workOrder
  printf "clean by both, git the committed file: work in another order %d, work not the committed file %d\n", moved, worse
  printf "clean by both, work the committed file and git not: %d\n", better
}' "$S/rows.txt"
