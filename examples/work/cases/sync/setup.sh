set -e
mkdir base
cd base
git init -q -b main .
printf 'one\n' > a.txt
git add a.txt
git commit -q -m one
cd ..
mkdir -p .ergo-work/base
: > .ergo-work/base/.registry
cp -a base .ergo-work/base/w
cd .ergo-work/base/w
git switch -q -c w
printf 'w work\n' > b.txt
git add b.txt
git commit -q -m 'w work'
cd ../../..
printf 'two\n' > base/a.txt
git -C base commit -q -am two
printf 'put\t"w"\t".ergo-work/base/w"\t"base"\t"w"\t"reflink"\tactive\t900\n' >> .ergo-work/base/.registry
