set -e
mkdir base
cd base
git init -q -b main .
printf 'hello\n' > a.txt
git add a.txt
git -c user.email=case@ergo.invalid -c user.name=case commit -q -m init
cd ..
mkdir -p .ergo-work/base
printf 'module .\n' > base/ergo.work
git -C base add ergo.work
git -C base -c user.email=case@ergo.invalid -c user.name=case commit -q -m work
: > .ergo-work/base/.registry
mkdir base/tmp
printf 'junk\n' > base/tmp/j
printf 'module .\nleave-out tmp\nwarm echo changed >> a.txt && test ! -e tmp && cat ../../../base/a.txt >&2; exit 3\n' > base/ergo.work
