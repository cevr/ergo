# laws-top

Spec A2 of law-top: a top-level law follows types.md Laws rules 2 and 4 to
10 as a member law does, with the scope "its parameters, then the package".
Each program gets exactly its markers, no cascade.

Ported from the Go test `TestTopLevelLawRules` (TP-DIAG), which named no
mutant. The programs are copies of `sketchtest.LawTopCases`
(`internal/sketch/sketchtest/laws.go`), which stays: the checker's fuzz
seeds and the lowering's erasure property (`lower/stage_lawtop_test.go`)
read it. A rule change edits both.
