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
cp -a base .ergo-work/base/a
printf 'put\t"a"\t".ergo-work/base/a"\t"base"\t"main"\t"reflink"\tactive\t900\n' >> .ergo-work/base/.registry
printf 'new\n' > .ergo-work/base/a/untracked.txt
