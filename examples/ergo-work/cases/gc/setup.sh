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
mkdir -p .ergo-work/base/.ergo-trash .ergo-work/other/.ergo-trash
cp -a base .ergo-work/base/.ergo-trash/a.10
cp -a base .ergo-work/other/.ergo-trash/x.1
printf 'put\t"a"\t".ergo-work/base/.ergo-trash/a.10"\t"base"\t"main"\t"reflink"\ttrashed\t10\n' >> .ergo-work/base/.registry
printf 'put\t"b"\t".ergo-work/base/b"\t"base"\t"main"\t""\tcreating\t990\n' >> .ergo-work/base/.registry
