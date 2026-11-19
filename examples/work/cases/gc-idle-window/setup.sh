set -e
# gc -idle reads the idle window on work's clock (WORK_NOW 260100, a
# 48h window from 87300), not from the record's since (900): a, whose
# files last changed at 1000, after its record but before the window, is
# idle and goes; b, with a file changed at 200000, inside the window,
# stays InUse (the verifier's M1).
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
cp -a base .work/base/b
printf 'put\t"a"\t".work/base/a"\t"base"\t"main"\t"reflink"\tactive\t900\n' >> .work/base/.registry
printf 'put\t"b"\t".work/base/b"\t"base"\t"main"\t"reflink"\tactive\t900\n' >> .work/base/.registry
find .work/base/a .work/base/b -exec touch -h -d @1000 {} +
touch -d @200000 .work/base/b/a.txt
