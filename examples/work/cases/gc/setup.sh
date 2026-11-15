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
mkdir -p .work/base/.work-trash .work/other/.work-trash
cp -a base .work/base/.work-trash/a.10
cp -a base .work/other/.work-trash/x.1
printf 'put\t"a"\t".work/base/.work-trash/a.10"\t"base"\t"main"\t"reflink"\ttrashed\t10\n' >> .work/base/.registry
printf 'put\t"b"\t".work/base/b"\t"base"\t"main"\t""\tcreating\t990\n' >> .work/base/.registry
