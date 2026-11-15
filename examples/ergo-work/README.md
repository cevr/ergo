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
- `init`, `noinit`, `badname`, `badconfig`, `taken`, `adopt`.
