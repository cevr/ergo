set -e
# With the untracked cache every new workspace keeps (core.untrackedCache),
# a file made after git status filled the cache still blocks rm: the held
# check's git status walks the folder whose time changed. The file is made
# in the same second as the cache, in a folder the cache already holds.
mkdir base
cd base
git init -q -b main .
mkdir src
printf 'hello\n' > src/a.txt
printf '[work]\n' > work.toml
git add src/a.txt work.toml
git -c user.email=case@ergo.invalid -c user.name=case commit -q -m init
cd ..
mkdir -p .work/base
: > .work/base/.registry
cp -a base .work/base/a
printf 'put\t"a"\t".work/base/a"\t"base"\t"main"\t"reflink"\tactive\t900\n' >> .work/base/.registry
git -C .work/base/a config core.untrackedCache true
git -C .work/base/a status --porcelain > /dev/null
git -C .work/base/a status --porcelain > /dev/null
printf 'new\n' > .work/base/a/src/untracked.txt
