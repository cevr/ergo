set -e
mkdir base
cd base
git init -q -b main .
printf 'one\n' > a.txt
git add a.txt
git commit -q -m one
printf 'module .\nwarm git rev-parse --abbrev-ref HEAD >&2; git log -1 --format=%%s >&2; cat a.txt b.txt >&2; exit 3\n' > ergo.work
cd ..
mkdir -p .ergo-work/base
: > .ergo-work/base/.registry
cp -a base .ergo-work/base/a
cd .ergo-work/base/a
git switch -q -c a
printf 'a work\n' > b.txt
git add b.txt
git commit -q -m 'a work'
printf 'a edit\n' >> a.txt
cd ../../..
printf 'put\t"a"\t".ergo-work/base/a"\t"base"\t"a"\t"reflink"\tactive\t900\n' >> .ergo-work/base/.registry
