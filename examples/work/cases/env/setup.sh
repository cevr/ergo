set -e
mkdir base
cd base
git init -q -b main .
printf '[work]\nenv = ["export GOCACHE={workspace}/.git/gocache", "export GOMEMLIMIT=6GiB", "umask 022"]\n' > work.toml
git add work.toml
git commit -q -m one
cd ..
mkdir -p .work/base
: > .work/base/.registry
cp -a base .work/base/w
printf 'put\t"w"\t".work/base/w"\t"base"\t"main"\t"reflink"\tactive\t900\n' >> .work/base/.registry
