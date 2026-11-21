set -e
# The base is a Rift copy: its marker .rift names it. new leaves the
# marker out (the Mac's WK-MARKER); the postcreate hook fails with status
# 7 when the workspace has it, so the answer shows a carried marker.
mkdir base
cd base
git init -q -b main .
printf 'hello\n' > a.txt
git add a.txt
git -c user.email=case@ergo.invalid -c user.name=case commit -q -m init
printf '01TESTMARKER\n' > .rift
cd ..
mkdir -p .work/base
: > .work/base/.registry
cmd='test ! -e .rift || exit 7'
printf "[hooks]\npostcreate = ['%s']\n" "$cmd" > base/work.toml
printf 'hooks.postcreate: %s' "$cmd" | (sha256sum 2>/dev/null || shasum -a 256) | cut -c1-64 > .work/base/.trusted
