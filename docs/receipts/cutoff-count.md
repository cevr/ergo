# Receipt: emitted-module cutoff count (CI-SELECT CS-CUTOFF-COUNT)

Measured 2026-11-18 on the Linux workbox (linux/amd64).

## Question

How often does a merged change actually change a program's emitted Go module? CS-CUTOFF would key an example's verdict on its emitted module (`ergo build --emitted`, `sketchfront.EmittedDigest`). The rule was to build it only if the median merge changes the module of less than half of the default examples.

## Method

- **Sample.** There are 145 batch-22 tip merges (`merge: … into wf10/batch-22 (tip N)`). I took 10 of them, evenly spaced by tip number: 5, 99, 160, 193, 228, 292, 348, 436, 502 and 525.
- **Per tip.** For both `tip^1` and `tip`:
  - check out the commit;
  - build `ergo` from that commit;
  - for every example whose `tags` file holds `default`, run `ergo build --emitted MAIN`. MAIN is `examples/X/cmd/X` for a module and `examples/X/main.ergo` otherwise, as in scripts/examples.
- **Count.** A program counts when it has a digest on both sides. It is "changed" when the two digests differ.
- **Settings.** `ERGO_GO=system`, an empty `ERGO_CACHE`, Go 1.27.4.

## Result

| tip | merge | tip^1 | programs | digest differs | share | what the merge changed in shipped code |
| --- | --- | --- | --- | --- | --- | --- |
| 5 | 6b2193f87 | 010e273a6 | 97 | 0 | 0% | 6 lower files |
| 99 | 3ece44bd8 | 2d5bfc5e8 | 95 | 0 | 0% | 5 lower files, 1 std |
| 160 | 4d5b1f3f7 | 64d838f6d | 97 | 97 | 100% | 7 runtime files (runtime/rt trace), 11 std |
| 193 | f76f07258 | 99dd62d5e | 98 | 0 | 0% | none |
| 228 | 2edf3042c | f0ee3dbf2 | 98 | 0 | 0% | 2 lower, 3 check, 4 std |
| 292 | fa508a96e | b627a308c | 95 | 95 | 100% | 2 runtime files (runtime/v1 context.go, files_gen.go), 4 std |
| 348 | c50e79726 | 598df0bc4 | 98 | 0 | 0% | none |
| 436 | 29e950591 | 8aca79796 | 99 | 0 | 0% | none |
| 502 | d7d42b0cb | d7fedafb7 | 98 | 0 | 0% | 1 lower file |
| 525 | 7cad0814d | 45760c4c9 | 101 | 0 | 0% | none |

- **Median programs changed per merge: 0 of about 98 (0%).** Eight of the ten merges change no default program's emitted module. Four of those eight changed the lowering.
- **The two merges that change every program are runtime edits.** `EmittedDigest` hashes the whole embedded runtime (`lower.Runtime()`), not only the runtime files a program links. So a change to any runtime file moves every digest.
- **Errors.** `fk-diag` does not check at three of the twenty commits (both sides of tip 5, and tip 160). It is left out of those tips' counts.

## Decision

The median is well under one half, so CS-CUTOFF is worth building: an example verdict keyed on its emitted module would be replayed after 8 of 10 merges.

**Follow-up.** Key the runtime part of the digest on the runtime files the program's module imports, not on the whole runtime. Without that, a runtime-only edit reruns every example even when the edited file is not linked.
