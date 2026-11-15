set -e
mkdir base
cd base
git init -q -b main .
printf 'one\n' > a.txt
git add a.txt
git commit -q -m one
cmd='git rev-parse --abbrev-ref HEAD >&2; git log -1 --format=%s >&2; cat a.txt b.txt >&2; exit 3'
printf "[hooks]\npostcreate = ['%s']\n" "$cmd" > work.toml
cd ..
mkdir -p .work/base
: > .work/base/.registry
printf 'hooks.postcreate: %s' "$cmd" | (sha256sum 2>/dev/null || shasum -a 256) | cut -c1-64 > .work/base/.trusted
cp -a base .work/base/a
cd .work/base/a
git switch -q -c a
printf 'a work\n' > b.txt
git add b.txt
git commit -q -m 'a work'
printf 'a edit\n' >> a.txt
cd ../../..
printf 'put\t"a"\t".work/base/a"\t"base"\t"a"\t"reflink"\tactive\t900\n' >> .work/base/.registry
