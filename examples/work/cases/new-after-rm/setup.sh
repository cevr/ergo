set -e
# A trashed a (rm a, no gc yet) does not hold its name: new a works and
# the trash stays listed as a~ for gc (the Mac's G8).
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
printf 'put\t"a"\t".work/base/.work-trash/a.10"\t"base"\t"main"\t"reflink"\ttrashed\t10\n' >> .work/base/.registry
mkdir -p .work/base/.work-trash/a.10
printf "old\\n" > .work/base/.work-trash/a.10/old.txt
