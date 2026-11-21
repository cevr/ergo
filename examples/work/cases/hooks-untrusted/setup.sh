set -e
mkdir base
cd base
git init -q -b main .
printf 'hello\n' > a.txt
git add a.txt
git -c user.email=case@ergo.invalid -c user.name=case commit -q -m init
cd ..
mkdir -p .work/base
: > .work/base/.registry
# init allowed an older work.toml; this one changed its command, so no
# hook runs until work trust (the hook would have written ran.txt and
# failed the case).
printf "[hooks]\npostcreate = ['echo allowed']\n" > base/work.toml
printf 'hooks.postcreate: echo allowed' | (sha256sum 2>/dev/null || shasum -a 256) | cut -c1-64 > .work/base/.trusted
printf "[hooks]\npostcreate = ['touch ran.txt; exit 3']\n" > base/work.toml
