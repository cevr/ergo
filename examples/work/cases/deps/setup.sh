set -e
mkdir base
cd base
git init -q -b main .
printf 'lock one\n' > bun.lock
git add bun.lock
git -c user.email=case@ergo.invalid -c user.name=case commit -q -m init
cmd='cat bun.lock >&2; echo installed > installed.txt'
printf "[deps]\nlock = \"bun.lock\"\ninstall = '%s'\n" "$cmd" > work.toml
cd ..
mkdir -p .work/base
: > .work/base/.registry
cp -a base .work/base/w
# w last installed from "lock one"; its lock changed since, so deps
# installs (and records the new digest).
one=$(printf 'lock one\n' | (sha256sum 2>/dev/null || shasum -a 256) | cut -c1-64)
printf 'put\t"w"\t".work/base/w"\t"base"\t"main"\t"reflink"\tactive\t900\t""\t""\t"w1"\t"%s"\n' "$one" >> .work/base/.registry
printf 'lock two\n' > .work/base/w/bun.lock
# The trust covers every command: the install, and the bun.lock preset's.
printf 'deps.install: %s\nclass.bun.lock.by: bun install --lockfile-only' "$cmd" | (sha256sum 2>/dev/null || shasum -a 256) | cut -c1-64 > .work/base/.trusted
