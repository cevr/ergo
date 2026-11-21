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
# a, b made on a, c made on b: rm -tree takes c, then b, then a.
for w in a b c; do cp -a base .work/base/$w; done
printf 'put\t"a"\t".work/base/a"\t"base"\t"a"\t"reflink"\tactive\t900\t""\t""\t"1111111111111111"\t""\n' >> .work/base/.registry
printf 'put\t"b"\t".work/base/b"\t".work/base/a"\t"b"\t"reflink"\tactive\t901\t""\t""\t"2222222222222222"\t""\n' >> .work/base/.registry
printf 'put\t"c"\t".work/base/c"\t".work/base/b"\t"c"\t"reflink"\tactive\t902\t""\t""\t"3333333333333333"\t""\n' >> .work/base/.registry
