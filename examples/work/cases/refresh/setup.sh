set -e
mkdir base
cd base
git init -q -b main .
printf 'one\n' > a.txt
printf 'two\n' > b.txt
printf 'three\n' > c.txt
git add a.txt b.txt c.txt
git -c user.email=case@ergo.invalid -c user.name=case commit -q -m init
git update-index -q --refresh
# Two files keep their text and get a new time: their stat in the index
# is stale. The third changes its text: a change, not a stale stat.
touch -t 200001010000 a.txt b.txt
printf 'changed\n' > c.txt
cd ..
mkdir -p .work/base
: > .work/base/.registry
