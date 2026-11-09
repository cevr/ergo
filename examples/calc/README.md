# calc

An expression language of ints and bools, with its lexer, parser, type checker, evaluator and constant-folding optimizer. `calc` reads one expression per line of standard input (or one from `-e`) and prints the expression, its value and its type. With `-fold` it folds constants first and prints the folded expression. A line that fails is told on standard error with its line number, and the status is 1 when any line failed.

```text
$ printf '1 + 2 * 3\nlet x = 6 in x * 7 < 50\n1 / (2 - 2)\n' | calc
1 + 2 * 3 = 7 : int
let x = 6 in x * 7 < 50 = true : bool
line 3: division by zero in 1 / (2 - 2)
calc: 1 of 3 expressions failed
```

The language: integer literals (64-bit, wrapping), `true`, `false`, variables, `-` and `!`, the binary operators `|| && < == + - * / %` (loosest first, each left-associative), `if C then A else B` and `let x = V in B`. `-5` with no space is a literal; `- 5` is a negation. A blank line and a line that starts with `#` are skipped.

Tag: `default`. It is an original program, not a port. Its goldens were made by running the program and then checked line by line by hand against the rules above.

## Layout

The program is a module of packages (`ergo.mod`: `module calc`), in the Go layout:

| folder | what it holds |
| --- | --- |
| `ast/` | The language. `expr.ergo` (the union `Expr` of eight variants, `Op`, `Type`, precedence and names), `print.ergo` (`Print`, with the fewest parentheses), `scope.ergo` (`Scope`, a list of typed variables), `gen.ergo` (`Gen`, `GenAny` and `Samples`, the generators the laws draw from), `tests.ergo`. |
| `syntax/` | `lex.ergo` (`Token`, `Lex`, the failure `BadChar`), `parse.ergo` (precedence climbing over a `*parser` borrow; `Unexpected`, `TooBig`, the row `Failure`), `laws.ergo`, `tests.ergo`. |
| `typing/` | `check.ergo` (`Check`; `Mismatch`, `Unbound`, the row `Failure`), `laws.ergo` (`Sound`), `tests.ergo`. |
| `eval/` | `eval.ergo` (`Value`, `Eval`, `Apply`, `Run` and its `Outcome`; `DivByZero`, `Stuck`, `Unset`), `tests.ergo`. |
| `opt/` | `fold.ergo` (the operation type `Optimizer` with two laws, its providers `Folder` and `Keep`, `Fold`), `laws.ergo`, `tests.ergo`. |
| `repl/` | The line protocol: `Answer` (parse, check, optimize, run) and `Run`, the only function that reads and writes. |
| `cmd/calc/` | The binary. `main.ergo` reads the flags and binds the host's streams, the input (the host's or the `-e` text) and the `Optimizer` that `-fold` chooses. |

## What it shows

- **A recursive union and exhaustive `match`.** `Expr` is eight variants, two of them binary and ternary over `Expr`. Every function over it names every variant, with no `case _` except over strings. The folder matches a tuple of an operator and two operands with literal field patterns: `case (ast.And, ast.BoolLit{V: true}, _), (ast.Or, ast.BoolLit{V: false}, _):`.
- **An operation type with laws.** `opt.Optimizer { Optimize(e) Expr, law PreservesMeaning(e, seed), law KeepsTheType(e, seed) }`. `ergo test` runs the laws against both providers, `Folder` and `Keep`. `main` chooses the provider from a flag: `func optimizer(fold bool) opt.Optimizer { if fold { opt.Folder } else { opt.Keep } }`, two types joined at the operation type.
- **Laws that tie the packages together.** `typing.Sound` (a well-typed expression does not get stuck: it ends in a value of its type or in a division by zero), `syntax.RoundTrip` (the text of an expression reads back as the expression), `syntax.ColumnsIncrease`, `opt.FoldIsIdempotent` and `opt.FoldNeverGrows`. `ergo test` runs them. A generated `Expr` seldom has valid names or a type, so the laws over expressions also take a `seed` and check `ast.Samples(e, seed)`: `e` and ten expressions that `ast.GenAny` (any shape) and `ast.Gen` (well-typed) build from `rand.Seeded(seed)`. A mutant of each law's subject is killed by its law (`calc-mutants.json` of the ex-upgrade unit: 3 of 3, seeds 1-3).
- **Typed failures in named rows.** `syntax.Failure = BadChar | Unexpected | TooBig` and `typing.Failure = Mismatch | Unbound` name the rows of `Parse` and `Check`; `Answer` fails `syntax.Failure | typing.Failure | eval.DivByZero`, and `turn` catches each variant by name, with no `case _`. `Eval`'s `Stuck` and `Unset` cannot follow a check that passed (law `Sound`), so `Answer` turns them into `Unreachable` defects with the reason. The folder never removes a part that can fail: `0 * (1 / 0)` stays, `false && 1 / 0 == 0` folds to `false` because `&&` short-circuits in `Eval` too, and folding uses `eval.Apply`, so a folded constant is the value `Eval` gives.
- **A borrow for parser state.** The parser is `type parser { toks []Token, pos int }`, owned by `Parse` as a `let mut` and lent to each rule as `*parser`, as Go's hand-written parsers do.
- **Capabilities as values.** `run` takes the host's standard input as a value of `io.Stdin` and binds it, or an `io.Input` of the `-e` text, in one context; the tests pass an `io.Input` and an `io.Recorder`.

## Run it

```sh
go build -o bin/ergo ./cmd/ergo
printf '1 + 2\n' | ./bin/ergo run examples/calc/cmd/calc -- -fold
./bin/ergo test examples/calc/opt
go run ./scripts/examples calc
```

## Cases

`session` (every construct, each failure kind, wrapping, short-circuit, comments and blank lines; status 1), `fold` (`-fold`: constant propagation through `let`, identities, and the divisions by zero that stay), `expression` (`-e`; standard input is not read), `help` (`-help`, status 0) and `usage` (a bad `-fold` value, status 2).

## Limits found

Each gap has its repro in the run dir's `examples-wave/gaps-ex-a.md`.

- **G-ex-a-3 (narrowed).** `Unreachable` says that `Stuck` and `Unset` cannot happen after a check, as a defect at run time; a typed tree that rules them out statically needs indexed types (GADTs).
- **G-ex-a-6 (bad diagnostic).** An S105 in package `syntax` named the failure `main.BadChar`, not `syntax.BadChar` (not rechecked here).

Closed in batch 12: G-ex-a-1 (S3: `BadChar` takes its character's size from `utf8.DecodeRuneInString`, where `charAt` read continuation bytes by hand), G-ex-a-2 (L1: hex literals; the byte test is gone with it), G-ex-a-3 in part (K: `Unreachable`), G-ex-a-4 (L2: named rows `syntax.Failure`, `typing.Failure`; `Answer`'s row of eight is three), G-ex-a-5 (`Lookup(s, "x") == Some{Value: BoolT}` checks), G-ex-a-7 and G-ex-a-8 (T1: `ergo run DIR -- ARGS`, no `dist/`), and the property tests that restated the laws (V1: the laws run; their steering moved into `ast.Samples`).
