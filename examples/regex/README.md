# regex

A small regular expression engine and a grep over it. `regex PATTERN` prints the lines of standard input that hold a match of PATTERN; `-x` matches whole lines, `-c` prints the count of matching lines, and `-r` runs the reference engine in place of the NFA. The status is 0 when a line matches, 1 when none does, and 2 on a usage or pattern error. The syntax is literals, `.`, classes (`[a-z]`, `[^0-9]`), `*`, `+`, `?`, `|`, groups and `\` escapes, over bytes.

Tag: `default`. It is an original program, not a port. Its goldens were written by hand, and `grep -E` (GNU grep 3) gives the same lines for the cases `match`, `count-whole`, `reference` and `pathological`.

## Layout

The program is a module of packages (`ergo.mod`: `module regex`), in the Go layout:

| folder | what it holds |
| --- | --- |
| `syntax/` | The pattern as a tree. `node.ergo` (the `Node` union, `Class.Has`, `Literal`, `Search`, `Size`), `parse.ergo` (`Parse` by recursive descent over a `*parser`; six typed failures and the named row `SyntaxError` that gathers them), `print.ergo` (`Print`, which parses back), `sample.ergo` (trees and texts from a seed, for the laws), `laws.ergo`, `tests.ergo`. It is pure. |
| `engine/` | The domain contract. `engine.ergo`: the operation type `Engine` (`Compile(pattern) Matcher`, five laws) and `Matcher` (`Matches(text) bool`, law `Repeatable`), the union `Mode` (`Whole`, `Inside`) and `Grep`, which compiles once and filters a `Seq` of lines. `checks.ergo`: the laws restated as functions over a provider value, for the providers' tests (`ergo test` runs `Engine`'s laws only for providers in package `engine`, G-exup-1). |
| `backtrack/` | The reference engine: it walks the tree with continuations (`func(j int) bool`) and tries each choice in turn. Short, obviously right, and exponential on `(a?)^n a^n`. |
| `nfa/` | The fast engine. `program.ergo`: Thompson's construction into a program of `Inst` (`Byte`, `AnyByte`, `Set`, `Split`, `Accept`). `machine.ergo`: the simulation as a machine, `type Simulation machine[Scan, Signal]`, whose state is the set of instructions it could be at and whose signals are `Read{C}` and `End`; `Step` is `Done` with `false` as soon as the set is empty. `laws.ergo`: `AgreesWithBacktrack`, `ProgramIsLinear`. |
| `cmd/regex/` | The binary. `main.ergo` reads the flags, parses the pattern, and binds `engine.Engine` to `nfa.Engine` (context `Fast`) or `backtrack.Engine` (context `Reference`); it is the only place that chooses an engine. |

## What it shows

- **Unions and exhaustive match.** The tree, the instructions, the signals and the parse failures are unions; every function over them names every variant. Trees compare with `==`.
- **Typed failures.** `Parse` fails `SyntaxError`, the named row `Unclosed | Unopened | NothingToRepeat | TrailingEscape | UnclosedClass | BadRange`; the parts of the parser fail the smaller row `nested`. `main` catches the row as one value and describes it with `Describe`.
- **A contract with laws and two providers.** `Engine` states what every engine keeps: a literal matches itself, a star matches the empty text, an Alt is either side, `p+` is `pp*`, a search finds a match inside any text. Both engines' tests run the same checks. The NFA's own law is that it agrees with the reference on every sampled pattern and text (four texts per pattern), and `SamplesMatchSometimes` shows that both answers occur. `ergo test` runs the laws; for each law a mutant of the program fails it (`regex-mutants.json` of the run, 12 of 12 killed).
- **A machine.** The NFA simulation is a machine over a state and a signal, and its input is the program: `Matches` starts it with `machines.Start(Simulation{}, program)`, moves with `machines.Next`, and stops at the first `Done`.
- **Contexts.** The binary chooses the engine by binding a context; the tests compare the two engines through the same `run`, with no mock.
- **Linear time.** `PathologicalPatternIsLinear` and the case `pathological` run `(a?)^30 a^30`, which the backtracker cannot finish in practice.

## Run it

```sh
go build -o bin/ergo ./cmd/ergo
./bin/ergo build examples/regex/cmd/regex -o /tmp/regex
printf 'the cat\nthe dog\n' | /tmp/regex 'c.t|d[aeiou]g'
./bin/ergo test examples/regex/nfa       # and syntax, backtrack, cmd/regex
go run ./scripts/examples regex
```

## Limits found

New gaps (in `gaps-ex-e.md` of the run):

- **G-ex-e-1**: `nfa`'s package function is `Build`, not `Compile`, because `Engine`'s `Compile` provider takes the name.
- **G-ex-e-2**: a function whose first parameter is the operation type `Engine` is not callable as `e.f(…)`; the checks are called `f(e, …)`.
- **G-ex-e-3**: no exit with a status and no message, so "no line matches" is written to standard error, which grep does not do.
- **G-ex-e-6**: measured on a loaded host before batch 12, `regex -c` is 17x to 24x slower than Go's regexp: each step of the value-state machine allocates.

Gaps that other lanes found, met here too:

- A byte has no conversion to text (G-ex-d-1): `ByteText` slices a 256-byte table, and the parse failures carry the source text of the operator or range.
- A property test cannot choose its generator (G-ex-d-4): the laws take an `int` seed and build trees with `Sample`.
- `ergo test` runs `Engine`'s and `Matcher`'s laws only for providers in package `engine` (G-exup-1), and a member law cannot be called (G-ex-c-5): each provider's tests call the `engine` check functions.
- A machine's state is a union (G-ex-c-16; measured: a record is S108 "a machine's state type must be a union"): `Scan` is a union of one variant.

Closed in batch 12: G-ex-e-4 (S2: `slices.Repeat`), G-ex-e-5 (W2: a pure function that appends has no hidden environment), G-ex-c-14 (L2: `fails SyntaxError`), G-ex-d-5 (W3: `Node` has `==`, `syntax.Equal` is gone), G-ex-d-11 (V2 and V4: the machine's input is the program, `machines.Start`/`machines.Next`), G-ex-b-2 (T3), and the property tests that restated the laws (V1: the laws run, with the tests' steering moved into them).
