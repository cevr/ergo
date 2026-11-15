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
printf 'put\t"a"\t".ergo-work/base/a"\t"base"\t"main"\t""\tcreating\t900\n' >> .ergo-work/base/.registry
printf 'put\t"b"\t".ergo-work/base/b"\t"base"\t"main"\t"reflink"\tactive\t905\n' >> .ergo-work/base/.registry
printf 'put\t"a"\t".ergo-work/base/a"\t"base"\t"main"\t"reflink"\tactive\t901\n' >> .ergo-work/base/.registry
printf 'drop\t"b"\n' >> .ergo-work/base/.registry
printf 'put\t"c"\t".ergo-work/ba' >> .ergo-work/base/.registry
