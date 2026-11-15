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
printf 'not committed\n' > note.txt
# What ready leaves: the snapshot tree (in an index of its own), kept by a
# commit at refs/work/latest and pushed to the base's refs/work/snapshots/w.
tree=$(GIT_INDEX_FILE=.git/ergo-work-index sh -c 'git read-tree HEAD && git add --all -- . && git write-tree')
commit=$(git commit-tree "$tree" -p HEAD -m 'ergo work snapshot')
git update-ref refs/work/latest "$commit"
git push -q ../../../base HEAD:refs/heads/w +refs/work/latest:refs/work/snapshots/w
cd ../../..
printf 'put\t"w"\t".ergo-work/base/w"\t"base"\t"w"\t"reflink"\tactive\t900\t""\t"%s"\n' "$tree" >> .ergo-work/base/.registry
