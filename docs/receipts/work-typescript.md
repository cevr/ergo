# work: TypeScript merges by declarations (SEMWS WG4)

2026-11-15, on the public Bun monorepo the work-vs-rift and work-locks receipts used (anomalyco/opencode, at 38bd73789a), with `plugins/typescript` (level 1, oxc-parser 0.141.0) as `[language.typescript]`. Instructions are user-mode instructions counted by `scripts/workcost/cnt` (perf counters), the measure that does not move with the host's load; wall times are under load and only for scale.

## The plugin is admitted on every file

`work plugin-test -max 0 -files '*.ts,*.tsx,*.mts,*.cts' "bun plugins/typescript/main.js"` in the checkout, load 46:

| | |
| --- | --- |
| files tested | 6,848 (every TypeScript file) |
| unreadable (oxc-parser errors) | 0 |
| laws held | `UnitsAreTheFile`, `KeysAreUnique`, `EditStaysInItsUnit` |
| instructions | 78.8 G (wall 25.6 s) |

Before this unit, plugin-test read every blob of the base by one `git cat-file` each, and its reading alone cost 22.9 G instructions (68 s) on this checkout. It now lists the files by name (`Vcs.Listed`, law `ListedIsTheSnapshot`), reads only those it tests, and reads blobs by one `cat-file --batch`: 0.90 G for all 6,848 files, 0.55 to 0.65 G for a sample (load 41 to 46). It asks the plugin for 256 files a request: the whole run went from 906 G (one plugin run per file) to 79 G, most of it now Bun parsing and JSON.

## Merge replay

`scripts/mergereplay/mergereplay.sh` replays each real two-parent merge of the history whose two sides both changed a `.ts` or `.tsx` file: 815 files of 37 merges. Each is merged by `git merge-file` and by `work merge-file auto`; a clean result is compared with the file the author committed.

| | Git (lines) | work (declarations) |
| --- | --- | --- |
| files left conflicted | 351 | 310 |
| clean and byte for byte the committed file | 458 | 472 |
| clean, the committed declarations in another order | 0 | 1 |
| instructions of the 815 merges, a file at a time (merge-file, Git's driver) | 1.9 G | 152.4 G (226.3 G before WG4b) |
| instructions of the 37 merges, a merge at a time (`work merge-folders`) | | 71.9 G |

Reading:

- 43 files Git leaves conflicted are clean for work: 16 are the committed file byte for byte, 1 is its declarations in another order. In the other 26 the author's resolution did more than merge: it removed an import the merged code no longer used, or kept one side's wording of a line both changed (a translation string). work keeps both sides' declarations; an unused import is a level-2 finding (WG5's checker), not a merge.
- No file both merge clean is the committed file by Git and not by work.
- 2 files Git merges clean are conflicted by work, `SameDeclaration`: a test file whose statements that declare nothing (`for (...) story(...)`) are keyed by kind and the second of that kind moved (`code ForOfStatement #2`); and a migration where theirs turned `export default {...}` into `const migration = {...}; export default migration` while ours edited the object, a rename the units merge does not see because a `default` unit stays. Both are known gaps.
- work's merges cost more than Git's because each parses three versions of the file and Git's reads no language. Each start of Bun with the plugin cost about 69 M instructions before SEMWS WG4b, when a file started it four times (base, ours, theirs, the merge cut again): 226 G. Now a Plugin knows its answers ahead (`work.Knowing`): merge-file asks for the three sides in one run and the merge in a second, 152 G; `work merge-folders` (the merge of three trees, `MergeTree`) asks for every file's sides in one run and every merge in one more, twice a merge however many files: 72 G, 38 times Git's. Every case merges the same by both (the script checks it: 0 apart).
- Of merge-folders' cost, most is the plugin: on the largest merge (94 files, 8.2 MB of requests) Bun's two runs are 5.6 G of 8.4 G, and in them oxc-parser's parse and the JSON of its AST are 90% (`--cpu-prof`); the split itself is under 5%. oxc-parser's raw transfer, which skips that JSON, runs on Node 22 and later but not on Bun, and Node's whole parse was slower than Bun's.
- Git's driver starts `work merge-file` once a file, so a merge by `git merge` (what `work sync` runs) is the 152 G line; a sync that merged by MergeTree would be the 72 G one (a follow-up).

The replay found and fixed, before these numbers: keys by a count among statements that declare nothing (each test of a test file is now its own unit, `code test "name"`); a unit renamed in place against a change of its body (law `RenamedUnitTakesTheOtherSidesChange`); units one side moved (law `UnitsCarryAMove`); and the line merge (`classes.MergeText`, so level 0 too) writing a block both sides added twice, or clashing on an insertion next to the other side's change, because it did not slide insertions as Git's diff does (laws `OneBlockBothAddedIsOnce`, `InsertionAfterAChangeStaysApart`).

Measured again 2026-11-15 with the binary of ad9bececd (load 38 to 53; the counts are instructions, which the load does not move). Run: `S=/workspaces/.scratch/<dir> WORK=<work binary> CNT=<cnt binary> sh scripts/mergereplay/mergereplay.sh <checkout>`; `MAX=N` reads the first N merges. The rows per file are in `S/rows.txt`, each case's sides in `S/cases/N`.
