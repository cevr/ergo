# The checker's diagnostic fixtures

Each folder is a family of Ergo programs that `ergo check --want` checks
([tooling](../../../../../docs/tooling.md)). A program is checked alone; the
diagnostics it gets, as `CODE@LINE`, must be exactly its `// want CODE`
markers, and a marker's quoted text (`// want S102 "requires B,"`) must be a
part of a message on that line. A code the checker does not register, or a
diagnostic with a negative length, matches no marker.

Until the gate runs Ergo's own tests (ST-0), `cmd/ergo`'s
`TestCheckerFixtures` runs the verb over every folder here, and the ledger
rows these programs kill (`internal/sketch/check/testdata/mutants.json`) name
it as their killer. Run one family by hand with

    ergo check --want internal/sketch/check/testdata/fixtures/<family>

A new family is a new folder with a README.md that says the rule it holds
and the mutants it kills.

| Folder | Was | Rule |
| --- | --- | --- |
| negative, positive, spec | `TestFixtures` | each program gets exactly its markers |
| every-row | `TestEveryRow` | a machine's `machines.Every` after charges `time.Clock` to the runner (FKG1) |
| fold-row | `TestFoldRow` | a machine's work row W is apart from the step's R (fumb-e, F1) |
| row-args | `TestRowArgFixtures` | a row argument is a written row `A \| B` (types.md, Generics 7; F8) |
| elided-rows | `TestElidedRowArgFixtures` | trailing row parameters may be left out (types.md, Generics 7; SS4a) |
| tail-failure | `TestAmbiguousTailFailure` | a tail value of an error type (rows.md, Tail results 3) |
| laws-top | `TestTopLevelLawRules` | top-level laws (law-top spec A2; types.md, Laws) |
| union-flat | (new, UNION-FLAT) | a union lists a union of its package; widening by subset; `case A as a` (types.md, Unions 1, 2 and 4) |
