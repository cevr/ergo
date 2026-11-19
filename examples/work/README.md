# work

The binary `work` (module `modules/work`, and `ergo work`): copy-on-write workspaces of any Git repository. `cmd/work/main.ergo` is the only place that chooses the providers: the Cloners that share blocks, in the order they are tried (a btrfs snapshot, a reflink per file, clonefile), the plain copy, the journal registry and its lock, the hooks by sh, git, and the host's files, commands and clock. The commands are the generated command line of `work.Api`; the module's README describes them.

```sh
ergo build examples/work/cmd/work -o ~/bin/work
cd /workspaces/ergo && work init
work new wf10/ws1               # {name, id, path: /workspaces/.work/ergo/wf10-ws1, ...}
work list
work rm wf10/ws1
```

The base is `WORK_BASE` when it is set (a relative one stays relative, so the paths the tool prints are relative too); else the base of the workspace the working folder is in, or the first folder up with a `.git`. `WORK_NOW`, a Unix second, fixes the clock. The list of bases is in `XDG_DATA_HOME/work` (else `~/.local/share/work`; a case's `HOME` is its folder).

## Cases

Each case's `setup.sh` makes a Git base (and, where the command reads them, a registry and workspaces written by hand: the registry is plain text) in the case's folder, and the case runs one command with `WORK_BASE=base` and `WORK_NOW=1000`.

- `new` clones with `-copy`, so it passes on a file system with no reflink too (it is still a reflink on btrfs and a clonefile on APFS). Its `postcreate` hook (allowed: setup.sh writes the digest work trust would keep) runs in the new workspace: it writes to the clone's `a.txt`, checks that `tmp` was left out, prints the base's `a.txt` on stderr and exits 3, so the `HookFailed` in new's answer shows that a write in the clone left the base as it was. The other hook cases print what they see the same way.
- `rm-held`, `rm-dirty`, `rm-abandon`: a clean workspace whose head the base holds goes to the trash; one with an untracked file is `NotHeld` unless `-abandon`. rm answers the list of what it removed.
- `rm-tree`: `rm -tree` of a workspace with a child that has a child removes the three, the leaves first.
- `gc` deletes the trashed workspace and keeps a reservation with no id (written before reservations had locks) made ten seconds before; another base's trash is beside it and stays.
- `list` replays a journal with a re-put, a drop and a line a crash cut short.
- `new-from`: the branch starts at the tag `v1`; the postcreate hook prints the branch, its commit, the file and the origin's push URL (the guard).
- `new-on`: a stack: the clone of workspace `a` holds `a`'s commit and its uncommitted edit, on branch `b`.
- `ready`: the workspace's origin is another repository; ready hands branch `w` and a snapshot holding the untracked `note.txt` to the base, and prints the commit the base's branch holds.
- `rm-ready`: a workspace with an untracked file whose snapshot ready handed (setup.sh does what ready does, with git) goes to the trash.
- `sync`: the base's `main` moved; sync merges it into `w` and prints the commit it merged.
- `merge-rows`: two lanes each added a row at the end of a ledger (a clash for Git's text merge) and one changed a row; merge-file merges them clean.
- `merge-clash`: both lanes changed one row apart, and one removed a row the other changed: merge-file names both rows and ends with status 1.
- `env`: the `env` lines of `work.toml` for workspace `w`.
- Their `env` files fix Git's author, committer and dates, so the commit and tree ids are the same on every run.
- `json-noinit`: with `--json` a failure's JSON is on standard output, and standard error says so.
- `new-carries`: a base with installed packages (a symlink among them), ignored build output and an untracked file; the hook lists the clone (all of it arrived), its Git status (the base's: the marker is not in it), the marker and the id the answer names, the stat settings of a clone made file by file, that the index is the base's byte for byte (the branch at HEAD wrote no index), the hook's environment, and the base's status (new wrote nothing into it).
- `hooks-untrusted`: work.toml changed after the digest was allowed: no hook runs, and new's answer says `Untrusted` with the commands.
- `trust`: the digest and the commands work trust allows.
- `deps`: the workspace's lock changed since its install: deps installs and prints the new digest.
- `refresh`: two files have a stale stat and one a change: refresh counts the two.
- `shell-init`, `import-rift` (a `.rift.toml` with two hooks).
- `init`, `noinit`, `badname`, `badconfig`, `taken`, `adopt`.
- `doctor` has no case: its answer names the host's file system and mount options.
