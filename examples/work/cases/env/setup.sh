set -e
mkdir base
cd base
git init -q -b main .
printf 'module .\nenv export GOCACHE={workspace}/.git/gocache\nenv export GOMEMLIMIT=6GiB\nenv umask 022\n' > ergo.work
git add ergo.work
git commit -q -m one
cd ..
mkdir -p .ergo-work/base
: > .ergo-work/base/.registry
cp -a base .ergo-work/base/w
printf 'put\t"w"\t".ergo-work/base/w"\t"base"\t"main"\t"reflink"\tactive\t900\n' >> .ergo-work/base/.registry
