#!/bin/sh
# refuse.sh: the refuse cases of docs/receipts/work.md (SEMWS WS9a), for
# Rift's native binary and work, each on its own fresh base: what each
# tool does when asked to remove work no one holds, a workspace another is
# made on, a workspace by the id of an earlier one of its name, the same remove
# twice, and a create where the disk cannot share blocks. It writes one
# row per case and tool on standard output: case, tool, exit status, the
# tool's first line, and what is left.
#
# Environment (all required):
#   S     a scratch folder on a disk that clones (btrfs, XFS, APFS) that
#         does not exist yet; each tool keeps its registry in it
#   M     a scratch folder on a disk that does not clone (tmpfs), that
#         does not exist yet
#   WORK  the work binary; RN Rift's native binary
set -eu
: "${S:?}" "${M:?}" "${WORK:?}" "${RN:?}"
for d in "$S" "$M"; do
	case "$d" in /*/*) ;; *) echo "refuse: $d is not a scratch path" >&2; exit 2 ;; esac
	[ ! -e "$d" ] || { echo "refuse: $d exists" >&2; exit 2; }
done
mkdir -p "$S/home" "$S/xdg" "$M"
export HOME="$S/home" XDG_DATA_HOME="$S/xdg" GIT_AUTHOR_NAME=r GIT_AUTHOR_EMAIL=r@x.invalid GIT_COMMITTER_NAME=r GIT_COMMITTER_EMAIL=r@x.invalid
set +e

# base DIR: a fresh Git base with one commit and a tracked work.toml.
base() {
	mkdir -p "$1" && git -C "$1" init -q -b main && printf 'x\n' >"$1/a.txt" && printf '[work]\n' >"$1/work.toml" &&
		git -C "$1" add a.txt work.toml && git -C "$1" commit -q -m one
}

# row CASE TOOL STATUS OUTPUT LEFT
row() { printf '%s\t%s\t%s\t%s\t%s\n' "$1" "$2" "$3" "$(printf '%s\n' "$4" | head -1 | cut -c1-160)" "$5"; }

# w CASEDIR ARGS...: work in the base of a case.
w() {
	c=$1
	shift
	(cd "$c/base" && "$WORK" "$@" 2>&1)
}
r() { "$RN" "$@" 2>&1; }
ex() { [ -e "$1" ] && echo kept || echo gone; }

echo "# refuse cases; load: $(cat /proc/loadavg); $(date -u +%FT%TZ)"

c=$S/dirty-work && base "$c/base" && (cd "$c/base" && "$WORK" init >/dev/null 2>&1 && "$WORK" new a >/dev/null 2>&1)
printf 'mine\n' >"$c/.work/base/a/mine.txt"
out=$(w "$c" rm a); row "rm of unheld work" work $? "$out" "mine.txt $(ex "$c/.work/base/a/mine.txt")"
c=$S/dirty-rift && base "$c/base" && r init "$c/base" >/dev/null && r create --copy-all --no-hooks --name a --into "$c/r" "$c/base" >/dev/null
printf 'mine\n' >"$c/r/a/mine.txt"
out=$(r remove --no-hooks "$c/r/a"); row "rm of unheld work" rift $? "$out" "mine.txt $(ex "$c/r/a/mine.txt") (at its path)"

c=$S/stack-work && base "$c/base" && (cd "$c/base" && "$WORK" init >/dev/null 2>&1 && "$WORK" new a >/dev/null 2>&1 && "$WORK" new -on a b >/dev/null 2>&1)
out=$(w "$c" rm a); row "rm of a workspace another is made on" work $? "$out" "a $(ex "$c/.work/base/a"), b $(ex "$c/.work/base/b")"
c=$S/stack-rift && base "$c/base" && r init "$c/base" >/dev/null && r create --copy-all --no-hooks --name a --into "$c/r" "$c/base" >/dev/null &&
	r create --copy-all --no-hooks --name b --into "$c/r" "$c/r/a" >/dev/null
out=$(r remove --no-hooks "$c/r/a"); row "rm of a workspace another is made on" rift $? "$out" "a $(ex "$c/r/a"), b $(ex "$c/r/b")"

c=$S/stale-work && base "$c/base" && (cd "$c/base" && "$WORK" init >/dev/null 2>&1)
old=$(w "$c" --json new a | sed -n 's/.*"id":"\([^"]*\)".*/\1/p')
w "$c" rm a >/dev/null && w "$c" gc >/dev/null && w "$c" new a >/dev/null
out=$(w "$c" rm -expect "$old" a); row "rm by the id of a removed workspace, when a new one has its name" work $? "$out" "the new a $(ex "$c/.work/base/a")"
row "rm by the id of a removed workspace, when a new one has its name" rift - "(Rift removes by path; it has no id)" -

c=$S/twice-work && base "$c/base" && (cd "$c/base" && "$WORK" init >/dev/null 2>&1)
id=$(w "$c" --json new a | sed -n 's/.*"id":"\([^"]*\)".*/\1/p')
w "$c" rm -expect "$id" a >/dev/null
out=$(w "$c" --json rm -expect "$id" a); row "the same rm twice (a retry)" work $? "$out" -
c=$S/twice-rift && base "$c/base" && r init "$c/base" >/dev/null && r create --copy-all --no-hooks --name a --into "$c/r" "$c/base" >/dev/null
r remove --no-hooks "$c/r/a" >/dev/null
out=$(r remove --no-hooks "$c/r/a"); row "the same rm twice (a retry)" rift $? "$out" -

c=$M/copy-work && base "$c/base" && (cd "$c/base" && "$WORK" init >/dev/null 2>&1)
out=$(w "$c" new a); row "new where the disk cannot share blocks" work $? "$out" "a $(ex "$c/.work/base/a")"
c=$M/copy-rift && base "$c/base"
out=$(r init "$c/base"); row "new where the disk cannot share blocks" rift $? "init: $out" "the base has $(ls -A "$c/base" | grep -c '^\.rift') .rift files"
echo "# end; load: $(cat /proc/loadavg)"
