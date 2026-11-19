# work against Rift (SEMWS WG0)

What: the cost of a workspace's create, remove and purge, and of the first `git status` in a new workspace, for Rift 0.0.13 through its npm wrapper (`rift`, what `workrift` runs), Rift's native binary, `work`, and `work` through the npm package's bin (`@cvr/work`, installed from the packed tarballs). Counts only: user-mode instructions (`perf_event_open`, children included), system calls (`strace -f -c`) and bytes (`btrfs filesystem du --raw`). Wall times in the raw rows are labelled loaded and are not claims.

How: `scripts/workcost/workcost.sh TREE` with `cnt` from `scripts/workcost/cnt`, on 2026-11-15, every run inside `systemd-run --user --scope -q -p MemoryMax=8G -p MemorySwapMax=0 -- nice -n 19`. The source is a btrfs subvolume, so both tools clone it with one snapshot. Rift ran with `--copy-all --no-hooks` (a full copy, as `work`'s default) and its own registry in the scratch folder. Loads at start: 47.9 (this repository) and 22.7 (the Bun monorepo).

Trees:
- this repository at `d3bb9dac`: 8,145 files, 8,118 tracked;
- the public Bun monorepo `anomalyco/opencode` at `38bd737`, installed with `bun install --frozen-lockfile --ignore-scripts`: 118,802 files, 11,479 tracked, 3.6 GB.

## Instructions (millions, the bounded scope's 3.4 M subtracted)

| Step | Tree | Rift wrapper | Rift native | work | work by npm bin |
| --- | --- | ---: | ---: | ---: | ---: |
| create | this repository | 108.4 | 1.4 | 10.4 | 10.4 |
| create | Bun monorepo | 108.6 | 1.5 | 10.6 | 10.6 |
| first `git status` in it | this repository | | 41.6 | 41.5 | |
| first `git status` in it | Bun monorepo | | 49.5 | 49.5 | |
| remove (to the trash) | this repository | 108.2 | 1.3 | 48.9 | 49.0 |
| remove (to the trash) | Bun monorepo | 108.6 | 1.3 | 63.4 | 63.3 |
| purge of the trash (3 workspaces) | this repository | | 1.0 | 35.4 | |
| purge of the trash (3 workspaces) | Bun monorepo | | 1.0 | 387.1 | |

`work` by the npm bin against the native binary, create: +0.1% (this repository, two runs each) and +0.4% (Bun monorepo), within the 5% asked: the bin is the native binary (`install.js` put it at `bin/work`; `cmp` equal), so no shell or Node process starts first.

## System calls and bytes

| Step | Tree | Rift native | work |
| --- | --- | ---: | ---: |
| create, system calls | this repository | 365 | 4,490 |
| create, system calls | Bun monorepo | 361 | 4,226 |
| remove, system calls | Bun monorepo | 300 | 22,319 |
| purge of 2, system calls (unlinkat) | Bun monorepo | | 616,783 (281,894) |
| create, exclusive bytes | both | 0 | 0 |

Both tools' new workspaces take the same bytes (Bun monorepo: total 3,643,641,856, exclusive 0) and their first `git status` reads the same files (Bun monorepo: 12,726 and 12,707 `newfstatat`, 0 status lines): the snapshot keeps the index fresh, and `work`'s branch at HEAD (two ref writes) rewrote no index.

## Reading

- Create: `work` uses 7 times more instructions than Rift's native binary (10.5 M against 1.5 M) and 10 times fewer than the wrapper `workrift` runs today (108.5 M). The rest of `work`'s create is its subprocesses (`test`, `btrfs`, `git`; strace's `-f` counts them): 4,200 system calls where Rift makes one ioctl among 360. The plan's design estimate was 2.7 M; the subprocesses are the difference, and an in-process clone and ref write would close it.
- Remove: `work rm` checks that the workspace's work is held by its parent (a `git status` and a snapshot, 48 to 63 M); Rift's remove checks nothing. Both are a rename.
- Purge: Rift's gc deletes each snapshot as a subvolume (one call: the mount has `user_subvol_rm_allowed`); `work gc` deletes the files one by one (`rm -rf`, 282,000 `unlinkat` for two Bun workspaces). The subvolume delete is WG1's purge.
- Not measured: Rift's default (filtered) create, which `work` does not offer; the hooks and installs, which neither ran.

Raw rows: the unit's run folder (`semws/WG0/workcost-ergo.txt`, `workcost-bunrepo.txt`); `scripts/workcost/workcost.sh TREE` rewrites them.
