# @cvr/work

`work`: copy-on-write workspaces of any Git repository. A workspace is a full clone of its base (tracked files, the Git folder, installed packages and build output) made by one copy-on-write call where the file system has one (a btrfs snapshot, a reflink per file, APFS clonefile), with a name, an id, a branch and a lifecycle. It replaces Rift.

```sh
npm install -g @cvr/work      # or: bun add -g @cvr/work
cd my-repo && work init
work new feature-x             # {"name":"feature-x","id":"…","path":"…/.work/my-repo/feature-x",…}
eval "$(work shell-init)"      # then work new changes to the new workspace
work list
work rm feature-x
```

Configuration is `work.toml` at the root of the repository; `work import-rift` prints one from a `.rift.toml`. Hooks and installs run only after `work trust`. The source and the full manual are in [modules/work](https://github.com/cevr/ergo/tree/main/modules/work).

The package holds no program of its own: its optional dependency for the platform (`@cvr/work-linux-x64`, `@cvr/work-linux-arm64`, `@cvr/work-darwin-arm64`) holds the static binary, and the install puts it at `bin/work`, so `work` starts no Node process. Where the install script does not run (bun, which blocks dependencies' postinstall by default, `bun add -g`, `npm install --ignore-scripts`), `bin/work` is a small sh launcher that follows its link and runs the platform package's binary (`scripts/npmwork-check.sh` installs the packed tarballs all three ways).
