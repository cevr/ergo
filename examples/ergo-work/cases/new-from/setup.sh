set -e
mkdir base
cd base
git init -q -b main .
printf 'one\n' > a.txt
git add a.txt
git commit -q -m one
git tag v1
printf 'two\n' > a.txt
git commit -q -am two
git remote add origin /nonexistent/loop-probe-x
printf 'module .\nwarm git rev-parse --abbrev-ref HEAD >&2; git log -1 --format=%%s >&2; cat a.txt >&2; git config remote.origin.pushurl >&2; exit 3\n' > ergo.work
cd ..
mkdir -p .ergo-work/base
: > .ergo-work/base/.registry
