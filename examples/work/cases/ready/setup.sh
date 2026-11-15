set -e
mkdir base
cd base
git init -q -b main .
printf 'one\n' > a.txt
git add a.txt
git commit -q -m one
cd ..
git init -q --bare up
mkdir -p .ergo-work/base
: > .ergo-work/base/.registry
cp -a base .ergo-work/base/w
cd .ergo-work/base/w
git remote add origin ../../../up
git switch -q -c w
printf 'w work\n' > b.txt
git add b.txt
git commit -q -m 'w work'
printf 'not committed\n' > note.txt
cd ../../..
printf 'put\t"w"\t".ergo-work/base/w"\t"base"\t"w"\t"reflink"\tactive\t900\n' >> .ergo-work/base/.registry
