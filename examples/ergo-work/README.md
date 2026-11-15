# ergo-work

The binary of `ergo work` (module `modules/work`): copy-on-write workspaces of one base checkout. `cmd/ergo-work/main.ergo` is the only place that chooses the providers: the Cloners that share blocks, in the order they are tried (a btrfs snapshot, a reflink per file, clonefile), the plain copy, the journal registry, git, and the host's files, commands and clock. The commands are the generated command line of `work.Api`; the module's README describes them.

```sh
ergo build examples/ergo-work/cmd/ergo-work -o ~/bin/ergo-work
cd /workspaces/ergo && ergo work init
ergo work new wf10/ws1          # prints /workspaces/.ergo-work/ergo/wf10-ws1
ergo work list
ergo work rm wf10/ws1
```

The base is `ERGO_WORK_BASE` when it is set (a relative one stays relative, so the paths the tool prints are relative too); else the base of the workspace the working folder is in, or the first folder up with a `.git`. `ERGO_WORK_NOW`, a Unix second, fixes the clock.

## Cases

Each case's `setup.sh` makes a Git base (and, where the command reads them, a registry and workspaces written by hand: the registry is plain text) in the case's folder, and the case runs one command with `ERGO_WORK_BASE=base` and `ERGO_WORK_NOW=1000`.

- `new` clones with `-copy`, so it passes on a file system with no reflink too (it is still a reflink on btrfs and a clonefile on APFS). Its `warm` command runs in the new workspace: it writes to the clone's `a.txt`, checks that `tmp` was left out, prints the base's `a.txt` on stderr and exits 3, so its `WarmFailed` shows that a write in the clone left the base as it was.
- `rm-held`, `rm-dirty`, `rm-abandon`: a clean workspace whose head the base holds goes to the trash; one with an untracked file is `NotHeld` unless `-abandon`.
- `gc` deletes the trashed workspace and keeps one whose clone started ten seconds before; another base's trash is beside it and stays.
- `list` replays a journal with a re-put, a drop and a line a crash cut short.
- `new-from`: the branch starts at the tag `v1`; the warm command prints the branch, its commit, the file and the origin's push URL (the guard).
- `new-on`: a stack: the clone of workspace `a` holds `a`'s commit and its uncommitted edit, on branch `b`.
- `ready`: the workspace's origin is another repository; ready hands branch `w` and a snapshot holding the untracked `note.txt` to the base, and prints the commit the base's branch holds.
- `rm-ready`: a workspace with an untracked file whose snapshot ready handed (setup.sh does what ready does, with git) goes to the trash.
- `sync`: the base's `main` moved; sync merges it into `w` and prints the commit it merged.
- `env`: the `env` lines of `ergo.work` for workspace `w`.
- Their `env` files fix Git's author, committer and dates, so the commit and tree ids are the same on every run.
- `json-noinit`: with `--json` a failure's JSON is on standard output, and standard error says so.
- `init`, `noinit`, `badname`, `badconfig`, `taken`, `adopt`.
