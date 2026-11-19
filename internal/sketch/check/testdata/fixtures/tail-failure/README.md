# tail-failure

A value of an error type that ends a function is a failure (rows.md, Tail
results 3), unless the declared result admits it. Since FK3-g1 a value of an
error type that is a variant of the function's one result is that result's
value when the function's written row does not declare it; lowering's
`TestErrorValueRuleTable` runs that rule. The programs here are the quiet
ones: `declared-is-quiet` (a written row that lists the type chose the
failure, so a caller handles it) and `error-union-stays` (since fs1, q27, a
result that is a union of error types makes the value the success value).

Ported from the Go test `TestAmbiguousTailFailure` (TP-DIAG), which named no
ledger mutant.
