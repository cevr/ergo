# Run-artifact mutant

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

# Lint spelling mutants (wf10 LINT0)

Each is an edit of a lint rule that matches by spelling, judged by hand: the edit applied, `go test -run 'TestLintRulesReadNoSpelling' ./internal/residue` and the rule's property in `./internal/sketch/lint` run, the file restored. Both survive the rule properties, whose controls cannot tell the spelled match from the fact on every input; the guard kills them.

| Mutant | Killer | Evidence |
| --- | --- | --- |
| `rule_foreign_error_public.go`: `check.IsGoError(n)` replaced by `n.String() == "go.Error"` | TestLintRulesReadNoSpelling | `rule_foreign_error_public.go:31: compares a spelling with a string`. |
| `rule_silent_catch.go`: a callee spelled `tally` counted as a trace (`fun.Name == "tally" \|\| check.CallAddsFootprint(info, n)`) | TestLintRulesReadNoSpelling | `rule_silent_catch.go:90: compares a spelling with a string`; TestSilentCatchAgreesWithReference stays green. |
