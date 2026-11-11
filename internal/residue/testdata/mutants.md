# Run-artifact mutant

## CF1 constant-aware std-name rule

`cf1-residue-literal-only`: change stdConst to return false unless its
argument is an ast.BasicLit. TestLoweringKeysOnNoStdName then calls the
pre-existing wirejson.go:isJSONText inventory row stale. The red before
the scanner fix is recorded in the CF1 run's red-residue.log. No previous
inventory row depended on the value of a named constant.

| Mutant | Killer | Evidence |
| --- | --- | --- |
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
