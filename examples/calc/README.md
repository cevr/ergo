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
| `ast/` | The language. `expr.ergo` (the union `Expr` of eight variants, `Op`, `Type`, precedence and names), `print.ergo` (`Print`, with the fewest parentheses), `scope.ergo` (`Scope`, a list of typed variables), `gen.ergo` (`Gen` and `GenAny`, the generators the property tests draw from), `tests.ergo`. |
| `syntax/` | `lex.ergo` (`Token`, `Lex`, the failure `BadChar`), `parse.ergo` (precedence climbing over a `*parser` borrow; `Unexpected`, `TooBig`), `laws.ergo`, `tests.ergo`. |
| `typing/` | `check.ergo` (`Check`; `Mismatch`, `Unbound`), `laws.ergo` (`Sound`), `tests.ergo`. |
| `eval/` | `eval.ergo` (`Value`, `Eval`, `Apply`, `Run` and its `Outcome`; `DivByZero`, `Stuck`, `Unset`), `tests.ergo`. |
| `opt/` | `fold.ergo` (the operation type `Optimizer` with two laws, its providers `Folder` and `Keep`, `Fold`), `laws.ergo`, `tests.ergo`. |
| `repl/` | The line protocol: `Answer` (parse, check, optimize, run) and `Run`, the only function that reads and writes. |
| `cmd/calc/` | The binary. `main.ergo` reads the flags and binds the host's streams, the input (the host's or the `-e` text) and the `Optimizer` that `-fold` chooses. |

## What it shows

- **A recursive union and exhaustive `match`.** `Expr` is eight variants, two of them binary and ternary over `Expr`. Every function over it names every variant, with no `case _` except over strings. The folder matches a tuple of an operator and two operands with literal field patterns: `case (ast.And, ast.BoolLit{V: true}, _), (ast.Or, ast.BoolLit{V: false}, _):`.
- **An operation type with laws.** `opt.Optimizer { Optimize(e) Expr, law PreservesMeaning(e), law KeepsTheType(e) }`. The laws hold for every provider; the tests run them against `Folder` and `Keep`, each bound in a context. `main` chooses the provider from a flag: `func optimizer(fold bool) opt.Optimizer { if fold { opt.Folder } else { opt.Keep } }`, two types joined at the operation type.
- **Laws that tie the packages together.** `typing.Sound` (a well-typed expression does not get stuck: it ends in a value of its type or in a division by zero), `syntax.RoundTrip` (the text of an expression reads back as the expression), `syntax.ColumnsIncrease`, `opt.FoldIsIdempotent` and `opt.FoldNeverGrows`. Each is restated as a property test over generated expressions: `ast.Gen` builds well-typed ones and `ast.GenAny` any shape, from `Random` bound to `rand.Seeded(seed)`, so a failing case replays from its seed. Each law has a mutant that its test kills (round-1 request).
- **Typed failures in rows.** `Answer` fails with eight failures of four packages. `turn` catches each by name, with no `case _`, and tells it. The folder never removes a part that can fail: `0 * (1 / 0)` stays, `false && 1 / 0 == 0` folds to `false` because `&&` short-circuits in `Eval` too, and folding uses `eval.Apply`, so a folded constant is the value `Eval` gives.
- **A borrow for parser state.** The parser is `type parser { toks []Token, pos int }`, owned by `Parse` as a `let mut` and lent to each rule as `*parser`, as Go's hand-written parsers do.
- **Capabilities as values.** `run` takes the host's standard input as a value of `io.Stdin` and binds it, or an `io.Input` of the `-e` text, in one context; the tests pass an `io.Input` and an `io.Recorder`.

## Run it

```sh
go build -o bin/ergo ./cmd/ergo
./bin/ergo build examples/calc/cmd/calc -o /tmp/calc && printf '1 + 2\n' | /tmp/calc -fold
./bin/ergo test examples/calc/opt
go run ./scripts/examples calc
```

## Cases

`session` (every construct, each failure kind, wrapping, short-circuit, comments and blank lines; status 1), `fold` (`-fold`: constant propagation through `let`, identities, and the divisions by zero that stay), `expression` (`-e`; standard input is not read), `help` (`-help`, status 0) and `usage` (a bad `-fold` value, status 2).

## Limits found

Each gap has its repro in the run dir's `examples-wave/gaps-ex-a.md`.

- **G-ex-a-1 (missing std op).** No rune decoder: no `utf8.DecodeRuneInString` and no `for` over the runes of a string. `syntax.charAt` reads the UTF-8 continuation bytes itself to name a non-ASCII character in `BadChar`.
- **G-ex-a-2 (missing language feature, #81).** No hexadecimal or binary integer literals: the continuation-byte test is written `src[j] >= 128 && src[j] < 192`, not `0x80` and `0xC0`.
- **G-ex-a-3 (missing language feature).** No way to say that a failure cannot happen. After `typing.Check` passes, `eval.Eval` cannot be `Stuck` or `Unset` (law `Sound`), but its row holds them, and Ergo has no `unreachable` and no defect a program can raise. `repl.turn` tells them as "internal" failures. A typed tree that rules them out needs indexed types (GADTs).
- **G-ex-a-4 (missing language feature).** A failure row has no name. `Answer`'s row of eight failures is spelled in full, and a function that describes any of them cannot take the row as a parameter: a union may not list records of other packages (a permanent rule, 196b Q8), so `type Problem = syntax.BadChar | …` is refused. The catch arms stand in `turn`.
- **G-ex-a-5 (checker).** `Lookup(s, "x") == Some{Value: BoolT}` infers `Some[BoolT]` from the field and does not take the type argument from the other operand, so the comparison is S108; the test writes `Some[Type]{Value: BoolT}`. In a return position `Some{Value: x}` takes the result type.
- **G-ex-a-6 (bad diagnostic).** An S105 in package `syntax` names the failure `main.BadChar`, not `syntax.BadChar`: a package's own types are named `main.` in its diagnostics.
- **G-ex-a-7 (tooling).** `ergo run DIR` passes no arguments to the program (there is no `--`), so a flag cannot be tried with `ergo run`; build with `ergo build -o` and run the binary.
- **G-ex-a-8 (tooling).** `ergo run cmd/calc` from the program's folder writes `dist/calc` there (gitignored), and then `go run ./scripts/examples` refuses the program: "unexpected dist/calc".
