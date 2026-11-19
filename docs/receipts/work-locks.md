# work: lock files are derived (SEMWS WG2)

`scripts/lockreplay/lockreplay.sh` on the history of a public Bun monorepo (anomalyco/opencode, 22,716 commits, at the checkout the work-vs-rift receipt used), 2026-11-15, load 34. Each case is a merge whose two sides both changed `bun.lock` apart. Git merges the lock and each `package.json` both sides changed by lines; work derives the lock from the merged manifests (the `bun.lock` preset, `bun install --lockfile-only`) and merges each manifest by key path (`work merge-file json`). The script does not run the package manager; that the derived lock is the merged manifests' is the law `LockIsTheManifests` (memory, a model package manager).

| | Part 1: real merges | Part 2: consecutive lock changes as if parallel |
| --- | --- | --- |
| cases | 8 | 1,321 |
| Git: conflict on `bun.lock` | 4 | 419 |
| Git: cases left conflicted (lock or a manifest) | 4 | 426 |
| work: cases left conflicted (a manifest key both changed apart) | 1 | 300 |
| manifests both sides changed | 69 | 5,029 |
| Git: manifests with a line conflict | 30 | 4,135 |
| work: manifests with a key conflict | 24 | 4,097 |
| work: clean manifest equal to the committed merge | 44 of 45 | (no committed merge) |

Reading:

- Of the four real merges Git leaves conflicted, three are clean for work: the lock is not merged, and the manifests merge by key; 20 of their 21 manifests are byte for byte the merge the author committed. The one that differs (4b83402a2, `packages/app/package.json`) is the same JSON value in another key order: theirs moved `"tailwindcss"` to its sorted place, and work keeps the order of ours (a move by one side is not carried; a follow-up).
- The fourth (5caf1f9f6) and most of part 2's conflicts are a release's `"version"` bump on both sides: the same key changed to two values, which work reports by key path (`version`) and never resolves to a side.
- Part 2 counts more cases than the plan's lockprobe run (288) because a case here only needs the lock at c2^ to be c1's, not c2^ = c1.

Run: `S=/tmp/opencode/<dir> WORK=<work binary> sh scripts/lockreplay/lockreplay.sh <checkout>`; the rows per case follow each part's sums.
