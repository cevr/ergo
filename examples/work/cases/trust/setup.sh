set -e
mkdir base
cd base
git init -q -b main .
printf 'hello\n' > a.txt
git add a.txt
git -c user.email=case@ergo.invalid -c user.name=case commit -q -m init
printf '[deps]\nlock = "bun.lock"\ninstall = "bun install --frozen-lockfile"\n[hooks]\npostcreate = ["bun run build"]\npreremove = ["bun run clean"]\n' > work.toml
cd ..
mkdir -p .work/base
: > .work/base/.registry
