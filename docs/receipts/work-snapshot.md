# work: a snapshot hashes only what changed (SEMWS WG4b)

`ready` and `rm` take a snapshot of the workspace's working files (`git.Snapshot`: an index of work's own, `.git/work-index`, filled from HEAD, then `git add --all` and `write-tree`). Before WG4b the index was read afresh from HEAD each time, with no stat data, so `git add` hashed every file of the checkout on every snapshot. Now HEAD is read into the kept index with `read-tree -m`, so an entry whose content HEAD still has keeps the stat data the last snapshot gave it, and `git add` hashes only the files changed since.

`scripts/workcost/snapcost.sh` runs those git commands, both ways, on the public Bun monorepo of the other work receipts (anomalyco/opencode at 38bd73789a, 11,479 files), 2026-11-15, load 47.9, counted by `scripts/workcost/cnt` (user-mode instructions):

| | first snapshot | second snapshot, nothing changed |
| --- | --- | --- |
| before (`read-tree HEAD`) | 7.22 G | 7.22 G |
| now (`read-tree -m HEAD`) | 7.23 G | 0.21 G |

Every run wrote the same tree. The first snapshot of a workspace still hashes every file: the kept index starts empty. Seeding it from the checkout's own index, whose stat data is fresh after a clone, would make the first one cheap too (a follow-up). An index `-m` cannot read is read afresh (`SnapshotReadsABadIndexAfresh`).

Run: `cnt -l m -- sh scripts/workcost/snapcost.sh <checkout> <tag> m`, twice with one tag; `plain` for the old way. It writes only `.git/snapcost-<tag>` and objects.
