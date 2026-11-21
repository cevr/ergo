# fold-row

A machine's work row is its own parameter W, apart from the step's R
(fumb-e, lane MB F1): a fold (`machines.Run`) runs no work, so it needs R
only, and the runner (`actor.StartMachine`) needs R | W.

Ported from the Go test `TestFoldRow` (TP-DIAG). Its comment named two
mutants that were never ledger rows (they sat in residue's
mutant-names allowlist): fold-needs-step-row-only (the work's row is unioned
into R again, so the law in `fold-needs-step-row-only.ergo` is S102) and
runner-needs-work-row (StartMachine leaves W out of its row, so the missing
`Codes` in `runner-needs-work-row.ergo` is quiet). Follow-up TP-DIAG-ROWS
gives them ledger rows.
