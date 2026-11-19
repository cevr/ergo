set -e
mkdir base
git -C base init -q -b main .
printf 'version = 1\n\n[hooks.postcreate]\nrun = "bun install --frozen-lockfile"\n\n[hooks.preremove]\nrun = "echo \\"bye\\""\n' > base/.rift.toml
