set -e
# The base is on main with "lock one" installed; its branch feature has
# "lock two". work new feature starts the workspace on feature: the clone
# carries the packages of lock one, so new installs (the Mac's G7).
mkdir base
cd base
git init -q -b main .
printf 'lock one\n' > bun.lock
git add bun.lock
git commit -q -m one
git switch -q -c feature
printf 'lock two\n' > bun.lock
git commit -q -am two
git switch -q main
cmd='cat bun.lock >&2; exit 0'
printf "[deps]\nlock = \"bun.lock\"\ninstall = '%s'\n" "$cmd" > work.toml
cd ..
mkdir -p .work/base
: > .work/base/.registry
printf 'deps.install: %s\nclass.bun.lock.by: bun install --lockfile-only' "$cmd" | (sha256sum 2>/dev/null || shasum -a 256) | cut -c1-64 > .work/base/.trusted
