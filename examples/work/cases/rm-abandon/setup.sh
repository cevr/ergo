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
cp -a base .work/base/a
printf 'put\t"a"\t".work/base/a"\t"base"\t"main"\t"reflink"\tactive\t900\n' >> .work/base/.registry
printf 'new\n' > .work/base/a/untracked.txt
