# sudoku

A sudoku solver. `sudoku` reads puzzles from standard input, one per line (81 cells, `.` or `0` for an empty cell; spaces, `|`, `-` and `+` are skipped), and prints each solution as a line of 81 digits, or as a picture with `-p`. `-r` runs the reference solver. A puzzle with no solution prints `unsolvable (N givens)`, and the status is then 1; a line that is not a puzzle stops the program with status 2.

Tag: `default`. It is an original program, not a port. The solutions in the cases `hard` and `reference` come from an independent hand-written Go solver (the bench control in the run dir) and agree with the published solutions of Norvig's grid 2, Inkala's 2012 puzzle and AI Escargot; the other goldens were written by hand.

## Layout

The program is a module of packages (`ergo.mod`: `module sudoku`), in the Go layout:

| folder | what it holds |
| --- | --- |
| `board/` | The domain. `grid.ergo` (`Grid` with construction authority, `Parse` with the typed failures `BadLength` and `BadCell`, `At`, `With`, `OfCells`, `Line`, `Picture`), `rules.ergo` (`Units`, `Peers`, `Consistent`, `Solves`, the failure `Unsolvable`, and the operation type `Solver` with four laws), `sample.ergo` (solved grids and puzzles from a seed), `checks.ergo` (the `Solver` laws as functions over a provider, for the providers' tests), `laws.ergo`, `tests.ergo`. |
| `propagate/` | The fast solver: candidate sets as bits, propagation (a cell with one digit left places it; a unit with one place left for a digit places it), and search on copies at the cell with the fewest digits. A dead end is the typed failure `Contradiction`, and the search backtracks by catching it. `laws.ergo`: `AgreesWithNaive`. |
| `naive/` | The reference solver: plain backtracking cell by cell over a `*[]int`, short enough to be the definition of a solution. |
| `cmd/sudoku/` | The binary. It binds `board.Solver` to `propagate.Solver` (context `Fast`) or `naive.Solver` (context `Reference`), and is the only place that chooses one. |

## What it shows

- **A contract with laws and two providers.** `board.Solver` states what every solver keeps: a solution solves the puzzle (`SolutionSolves`), the solver is deterministic, a solved grid is its own solution, and a puzzle with a digit twice in a unit has no solution. Both solvers' tests run the same checks; the fast solver's own law is that it finds a solution exactly when the reference does.
- **Typed failures as control flow.** `propagate`'s search is `search(t) catch { case Contradiction: continue }`; in Go, that is an error return, with no panic and no recover.
- **Construction authority.** Only `board` builds a `Grid`, so every `Grid` has 81 cells of 0 to 9.
- **Exhaustive match** on the parse failures and on the flags, and contexts for choosing the solver in `main` and in the tests.

## Run it

```sh
go build -o bin/ergo ./cmd/ergo
./bin/ergo run examples/sudoku/cmd/sudoku -- -p < examples/sudoku/cases/picture/stdin
./bin/ergo test examples/sudoku/propagate   # and board, naive, cmd/sudoku
go run ./scripts/examples sudoku
```

## Limits found

New gaps (in `gaps-ex-e.md` of the run):

- **G-ex-e-7**: no fixed arrays. `Grid` holds a slice, a copy appends 81 cells, and the length is checked at run time by `OfCells`. Measured on a loaded host, 1,000 hard puzzles take 0.49 s against 0.28 s for the same algorithm in hand Go with `[81]uint16` (1.75x).
- G-ex-e-8 closed in batch 12: `count` and `lowest` use std `bits.OnesCount` and `bits.TrailingZeros`.
- G-ex-e-5 closed in batch 12: a slice is a value, so `copied` is gone (`let mut t = s`) and `With` sets one cell of its copy.

Gaps that other lanes found, met here too:

- A failure that cannot happen cannot be stated so (G-ex-a-3): both solvers catch `OfCells`'s `BadLength` and `BadCell` and return `Unsolvable`.
- G-ex-d-5 closed in batch 12: grids compare with `==`, and `board.Equal` is gone.
- A prelude name cannot be a local (G-ex-c-11): the line counter cannot be named `number`, the name of the numeric constraint.
- A value of an error type in a `catch` arm is not a failure; the arm must `return` it (G-ex-c-3).
- The `Solver` laws run only for providers in `board` (G-exup-1), a member law's check cannot be shared (G-ex-c-5), and a property test cannot choose its generator (G-ex-d-4): the tests take seeds and build puzzles with `Solution`, `Puzzle` and `Broken`.
- A package-level `Map` is global mutable state that a pure function can write (G-ex-b-18): the mutant `sd-determ` breaks the `Deterministic` law that way, and the checker accepts it.
