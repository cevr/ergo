# Run-artifact mutant

| Mutant | Killer | Evidence |
| --- | --- | --- |
| Restore the os package identity range check, or SealedStd's extern-result rule in the checker | TestCheckerNamesNoStdPackage | STD1a red acceptance, empty identity inventory. |
| Reintroduce the tracked `tmp/orchestrate/wf10/batch-15/fix-r7/round-7-request.md` from d786905a | TestNoRunDirFilesTracked | Red before deletion in batch-15 fix-r8/red-residue.log. This is a Git index mutation, not a text replacement supported by scripts/mutants. |

# Runtime-waits rule mutants (wf10 MK1)

Each is an edit of `unparkedWaits` in `runtime_waits_test.go`, judged by hand (the residue package has no JSON ledger): the edit applied, `go test -run 'TestRuntimeWait' ./internal/residue` run, the file restored.

| Mutant | Killer | Evidence |
| --- | --- | --- |
| A runtime/v1 wait reverted to a raw one (`mk1-waitkids-unparked`, `mk1-halt-unparked` in runtime/v1's ledger) | TestRuntimeWaitsPark | The function is flagged: it waits and calls no rt.Park. |
| Receives not counted (`n.Op == token.ARROW` never true) | TestRuntimeWaitsPark | The rows `Actor.open` and `Actor.arm` admit nothing: stale. |
| Selects with no default not counted | TestRuntimeWaitsPark | A row goes stale. |
| A select with a default counted as a wait | TestRuntimeWaitsPark | `mailbox.Wake` and `deadline.Stop` are flagged. |
| `Wait()` calls not counted | TestRuntimeWaitsPark | The rows `machineStart` and `Task.Wait` go stale. |
| `rt.Park` not seen | TestRuntimeWaitsPark | Every parked wait (`Actor.await`, `Queue.wait`, …) is flagged. |
| Function literals not entered (`mk1-residue-skips-literals`) | TestRuntimeWaitRule | Survives TestRuntimeWaitsPark; the rule test's literal is not flagged. |

# Unsafe inventory mutants (wf10 STD-sh r2)

Each applied by hand (an edit, `go test -run TestUnsafeBodiesListed ./internal/residue`, the file restored), 2026-11-12: all killed.

| Mutant | Killer | Evidence |
| --- | --- | --- |
| The `StringsBuilderTextOwned` line names another function (the body is unlisted) | TestUnsafeBodiesListed | `runtime/v1/owned.go StringsBuilderTextOwned uses package unsafe and is not in …` |
| `StringsBuilderTextOwned` returns `string(buf)` (the line goes stale) | TestUnsafeBodiesListed | `names no use of unsafe (a stale line)` |
| The review part of a line removed | TestUnsafeBodiesListed | the line format error |

# Unsafe inventory over every package (wf10 FU-audit, audit 3 G8)

Each applied by hand as above, 2026-11-12: all killed. Red at the base (19e38b73f, the rule widened, no rows added): 16 uses outside runtime/v1 unlisted, `runtime/rt/trace.go identOf` among them (run dir `fu-audit/red-unsafe.log`).

| Mutant | Killer | Evidence |
| --- | --- | --- |
| `runtime/rt/trace.go identOf` unlisted (the line renamed); the runtime/v1-only scan of STD-sh lets it survive | TestUnsafeBodiesListed | `runtime/rt/trace.go identOf uses package unsafe and is not in …` |
| `goPackageFile` reads testdata and `_`/`.` directories too | TestUnsafeBodiesListed | 270 uses flagged, the bindc fixtures' generated code among them |
