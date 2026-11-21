set -e
# A driver class whose command fails: merge-file auto names the path's
# driver and fails (status 1), so Git keeps the file conflicted; its
# candidate is never taken. The driver is allowed (work trust).
mkdir -p base .work/base
cmd='echo driven >&2; exit 3'
printf '[[class]]\nmatch = "*.txt"\nkind = "driver"\ndriver = '"'"'%s'"'"'\n' "$cmd" > base/work.toml
printf 'class.*.txt.driver: %s' "$cmd" | (sha256sum 2>/dev/null || shasum -a 256) | cut -c1-64 > .work/base/.trusted
printf 'one\n' > o.txt
printf 'ours\n' > a.txt
printf 'theirs\n' > b.txt
