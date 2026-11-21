set -e
# adopt-rift with no Rift registry at the path -registry names: it says
# which file it could not read and adopts nothing.
mkdir base
cd base
git init -q -b main .
printf 'hello\n' > a.txt
git add a.txt
git -c user.email=case@ergo.invalid -c user.name=case commit -q -m init
cd ..
mkdir -p .work/base
: > .work/base/.registry
