# row-args

A row argument of a row parameter is written as `needs` and `fails` write a
row, `A | B`, in a type's type arguments and in an expression's index alike
(types.md, Generics 7; lane MB F8).

Ported from the Go test `TestRowArgFixtures` (TP-DIAG). Its comment named
five mutants, one per program, that were never ledger rows (they sat in
residue's mutant-names allowlist, except row-in-parentheses): row-in-type
(the parser reads only the first element of a type argument),
row-keeps-every-element (the checker keeps only the first element of a row
argument), row-in-index (the checker refuses the expression index's row),
row-for-a-type (a capability row where a type is expected, which has no
value type; owner q47) and row-in-parentheses (the parenthesised row's
message, as `needs (A | B)` has one; its marker's quoted text holds the
message). Follow-up TP-DIAG-ROWS gives them ledger rows.
