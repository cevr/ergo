set -e
mkdir base
cd base
git init -q -b main .
printf 'hello\n' > a.txt
git add a.txt
git -c user.email=case@ergo.invalid -c user.name=case commit -q -m init
cd ..
mkdir -p .work/base
printf '[work]\n' > base/work.toml
git -C base add work.toml
git -C base -c user.email=case@ergo.invalid -c user.name=case commit -q -m work
: > .work/base/.registry
mkdir base/tmp
printf 'junk\n' > base/tmp/j
cmd='echo changed >> a.txt && test ! -e tmp && cat ../../../base/a.txt >&2; exit 3'
printf "[work]\nleave_out = [\"tmp\"]\n[hooks]\npostcreate = ['%s']\n" "$cmd" > base/work.toml
printf 'hooks.postcreate: %s' "$cmd" | (sha256sum 2>/dev/null || shasum -a 256) | cut -c1-64 > .work/base/.trusted

