# work: parity with Rift (SEMWS WS9a)

What: the parity table of the plan's §3 for the Ergo run's lanes, Rift (0.0.13: its npm wrapper `rift`, what `workrift` runs, and its native binary) against `work` at the lane head `d1a69888b` (gc -idle; WG1's subvolume purge). Each row says what the lane command is in each tool; the measured rows give the clone cost, the exclusive bytes and the refuse cases, on the same host, with the load noted. The first measurement is in `work-vs-rift.md` (WG0, before WG1's purge).

How, on 2026-11-15 at 11:41Z and 11:52Z:
- `scripts/workcost/workcost.sh TREE`, with `cnt` from `scripts/workcost/cnt`. Every run is inside `systemd-run --user --scope -q -p MemoryMax=8G -p MemorySwapMax=0 -- nice -n 19`, and each starts at a 1-minute load under 48: 46.6 for this repository, 46.7 for the Bun monorepo. The source is a btrfs subvolume, so both tools clone it with one snapshot. Rift ran with `--copy-all --no-hooks`, a full copy like `work`'s default.
- `scripts/workcost/refuse.sh` for the refuse cases, at a load of 50.0. It runs each tool on its own fresh base: on btrfs, and on tmpfs for the disk that cannot clone.
- The trees:
  - this repository at `d1a69888b`: 8,578 files, 8,551 tracked;
  - the public Bun monorepo `anomalyco/opencode` at `38bd737`, installed: 118,803 files, 11,479 tracked, 3.6 GB.
- The npm bin leg was not rerun. WG0 found that it is the same binary (`cmp` equal), so its row is `work`'s.

## The parity table

| Rift, `workrift`, `lane-rift.sh` | `work` (`ergo work`), `scripts/lanework/lanework.sh` | Measured here |
| --- | --- | --- |
| `rift init`, a `.rift` marker, a SQLite registry | `work init`; the repository tracks its `work.toml`, so init writes nothing into the base; a journal registry with the branch, base, state and since of each workspace | the refuse cases below |
| `rift create --copy-all` (btrfs snapshot, `FICLONE`, `clonefile`) | `work new` with the same three clone calls; a plain copy only with `-copy` | clone cost and bytes below |
| `lane-rift.sh create B X`: fetch, fast-forward the source, branch | `lanework.sh create B X`: the same fetch and fast-forward, then `work new -branch B -from REV B` | |
| `. .git/lane.env` | `lanework.sh env` writes the same three lines to `.git/lane.env` | |
| `lane-rift.sh push`: the branch into the main checkout, never origin | `lanework.sh push`: the same push and the same push URL guard; `work ready` hands into the parent | |
| `rift list`, `ancestors` | `work list` (`--json`): name, id, path, parent, branch, cloner, state, since | |
| `rift remove` to `.trash`: the whole subtree, by path, with no check | `work rm NAME -expect ID`: one leaf, the id given, a retry that answers `already`; it refuses work not held in the parent. `lanework.sh remove` checks the push as `lane-rift.sh` did, then runs `work rm -abandon -expect ID` | remove cost and refuse cases below |
| `rift gc`: the trash of every repository | `work gc [-older AGE] [-id ID]`: this base's trash only, by age or one id; a btrfs workspace is deleted as a subvolume | purge cost below |
| `workrift prune`: archives and removes after two idle days | `work gc -idle 48h` removes the idle workspaces whose work is held in their parent and reports the others, with the reason; with `-archive` (WG6a) it keeps an unheld one's work in the base at `refs/work/archive/NAME/ID`, then removes it too: the daily timer is `work gc -idle 48h -archive` | the refuse cases below; WG6a's acceptance check (the kept tree has the untracked file) |
| `sudo btrfs filesystem du` by hand | `work du` | bytes below |
| the running Rifts of a wave | `work adopt DIR` and `work import-rift` register them as workspaces; `work adopt-rift` (WG6a) registers every Rift copy of the base from Rift's registry, a copy made on a copy with its parent, moving no file; `work doctor -rift` lists the copies left | WG6a's acceptance check: two stacked Rift copies adopted, their trees' listings (name, inode, size, time) unchanged, then none left |

## Clone cost (millions of user-mode instructions, the bounded scope's 3.4 M subtracted)

| Step | Tree | Rift wrapper | Rift native | work |
| --- | --- | ---: | ---: | ---: |
| create | this repository | 108.6 | 1.4 | 10.6 |
| create | Bun monorepo | 108.6 | 1.5 | 10.8 |
| first `git status` in it | this repository | | 43.6 | 43.5 |
| first `git status` in it | Bun monorepo | | 49.6 | 49.6 |
| remove (to the trash) | this repository | 108.2 | 1.3 | 51.3 |
| remove (to the trash) | Bun monorepo | 108.5 | 1.3 | 63.9 |
| purge of the trash (3 workspaces) | this repository | | 1.0 | 10.2 |
| purge of the trash (3 workspaces) | Bun monorepo | | 1.0 | 10.3 |

`work`'s create is the mean of two runs; the two runs differ by less than 1.5%.

## System calls and bytes

| Step | Tree | Rift native | work |
| --- | --- | ---: | ---: |
| create, system calls | this repository | 365 | 4,362 |
| create, system calls | Bun monorepo | 365 | 4,328 |
| remove, system calls | Bun monorepo | 304 | 22,410 |
| purge of 2, system calls | Bun monorepo | | 2,838 |
| create, total and exclusive bytes | this repository | 84,221,952, 0 | 84,221,952, 0 |
| create, total and exclusive bytes | Bun monorepo | 3,643,641,856, 0 | 3,643,641,856, 0 |

## Refuse cases

| Case | Rift | work |
| --- | --- | --- |
| rm of a workspace with work its parent does not hold (an untracked file) | removes it (status 0); the file goes with it | `NotHeld` (status 1); the file stays |
| rm of a workspace another is made on | removes both (status 0) | `HasDescendants` with the other's path (status 1); both stay |
| rm by the id of a removed workspace, when a new one has its name | no ids: a remove by path takes the new one | `already` for the old id (status 0); the new one stays. After the 30-day tomb window it is `IdMismatch` |
| the same rm twice (a retry after a lost answer) | the second fails: "No such file or directory" (status 1) | the second answers `already` (status 0) |
| new where the disk cannot share blocks (tmpfs) | refused at `rift init`: "copy-on-write cloning unavailable" | init works; `new` is `CopyRefused` with the bytes a copy would take (status 1); `-copy` allows the copy |
| an idle workspace whose work is not held | `workrift prune` archives it, then removes it | `gc -idle` keeps it and reports `removed: false, why: NotHeld` (the WS9a acceptance check) |

## Reading

- Create: `work` uses 10.7 M instructions, 7 times Rift native's 1.5 M and 10 times fewer than the wrapper `workrift` runs (108.6 M). The rest is `work`'s subprocesses (`test`, `btrfs`, `git`), as in WG0. The bytes are the same: a new workspace takes no exclusive byte in either tool.
- Remove: `work rm` checks that the parent holds the workspace's work, with a `git status` and a snapshot (51 to 64 M). Rift checks nothing, and the refuse cases show what that costs: unheld work and a stacked workspace go with the remove.
- Where rm's cost goes (Bun monorepo, WS9a's question): the held check's `git status` is 51 of rm's 64 M, and its walk of the untracked files about 32 M of that; the floor of a `git status` that is exact is the stat of each tracked file (`status -uno`, 19 M). Rift's 1.3 M is a remove with no check.
- The untracked cache (WG6a): `work new` turns on `core.untrackedCache` in each workspace, so a `git status` after the first walks only the folders whose time changed. `scripts/workcost/rmcache.sh` (2026-11-15, load 38), `work rm` after one `git status` in the workspace, the cache off (before) and on (after), the scope's 3.4 M not subtracted:

  | | before | after |
  | --- | --- | --- |
  | the held check's `git status` | 52.8 M | 31.8 M |
  | `work rm`, two runs | 66.9 M, 67.1 M | 46.4 M, 46.3 M |
  | `work rm`, system calls | 22,109 | 17,404 |

  The answer is unchanged: the case `rm-untracked-cached` makes an untracked file in a folder the cache holds, in the same second, and rm still refuses it (`NotHeld`). Going below the tracked-stat floor needs a change journal (fsmonitor or watchman).
- Purge: WG1's subvolume delete brings `work gc` from 35.4 M and 616,783 system calls (WG0, three workspaces of the Bun monorepo) to 10.3 M and 2,838. That is one `btrfs subvolume delete` a workspace in place of one `unlinkat` a file. Rift's gc is still 10 times fewer instructions: it deletes with no subprocess.
- Not measured: Rift's default (filtered) create, which `work` does not offer, and the hooks and installs, which neither tool ran.

Raw rows: the unit's run folder (`semws/WS9a/workcost-ergo.txt`, `workcost-bunrepo.txt`, `refuse.txt`; `semws/WG6a/rmcache-bunrepo.txt`). The scripts rewrite them.
