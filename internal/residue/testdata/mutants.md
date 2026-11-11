# Run-artifact mutant

| Mutant | Killer | Evidence |
| --- | --- | --- |
| Restore the os package identity range check, or SealedStd's extern-result rule in the checker | TestCheckerNamesNoStdPackage | STD1a red acceptance, empty identity inventory. |
| Reintroduce the tracked `tmp/orchestrate/wf10/batch-15/fix-r7/round-7-request.md` from d786905a | TestNoRunDirFilesTracked | Red before deletion in batch-15 fix-r8/red-residue.log. This is a Git index mutation, not a text replacement supported by scripts/mutants. |
