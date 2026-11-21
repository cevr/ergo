# elided-rows

The rule of types.md, Generics 7 (std-strengths SS4a): a generic type's
trailing row parameters may be left out of its written type arguments, and
each one left out is the empty row. A type parameter, a row parameter that a
written argument follows, and an extra argument stay S108; a generic type
with no written argument stays S108 as well. Each program checks with no
diagnostic, or gets exactly its markers.

Ported from the Go test `TestElidedRowArgFixtures` (TP-DIAG), which named
no mutant. The seeded property beside it in
`internal/sketch/check/elided_rows_test.go` stays in Go.

prints-no-fails.ergo (KSIG-FALLIBLE): a signature prints by what its identity
compares, so Build of a Command[int], its row left out, is func() int, with
no bare fails. It kills the ledger mutant sig-prints-bare-fails.
