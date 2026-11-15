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
printf 'put\t"a"\t".work/base/a"\t"base"\t"main"\t""\tcreating\t900\n' >> .work/base/.registry
printf 'put\t"b"\t".work/base/b"\t"base"\t"main"\t"reflink"\tactive\t905\n' >> .work/base/.registry
printf 'put\t"a"\t".work/base/a"\t"base"\t"main"\t"reflink"\tactive\t901\n' >> .work/base/.registry
printf 'drop\t"b"\n' >> .work/base/.registry
printf 'put\t"c"\t".work/ba' >> .work/base/.registry
