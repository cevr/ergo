# quiz

A guided picker. Questions narrow a catalogue down to the items that fit every answer, and the user picks one of the results. A script on standard input plays the screen: its first line names the question set, and each other line is one answer, `back`, `pick N` or `restart`. The program shows each question with the number of items that each choice keeps, or the results.

Tag: `default`. It is an original program, a neutral re-creation of a common pattern (a class A machine whose "calculating" states only pick the next state). Its goldens were made by running the program and then checked line by line against the rules below by hand.

## Rules

- A question set is a list of questions and a catalogue of items. An item has tags. A choice keeps the items that carry its tag; a choice with no tag ("no preference", "either") keeps every item.
- The items left are the items that every answer keeps.
- A question is asked only when it can narrow what is left: some choice keeps some of the items left, but not all. Any other question is skipped. When no later question can narrow, or at most one item is left, the results show. A set whose first question cannot narrow starts at a later one.
- A choice is a number. It must be one the question offers, and it must keep at least one item left; these guards are tried in that order.
- `back` takes back the last answer and asks its question again. Skipped questions are not asked again. `back` before any answer is refused.
- `pick N` picks the Nth result, while the results show. It ends the quiz with the item and the labels of the answers given. Lines after the pick are not read.
- `restart` forgets every answer.
- Exit status: 0 after a pick, 1 when the set does not open, 2 when the input ends before a pick.

## Layout

The program is a module of packages (`ergo.mod`: `module quiz`), in the Go layout:

| folder | what it holds |
| --- | --- |
| `quiz/` | The domain package. `catalogue.ergo` (`Item`, `Choice`, `Question`, `Set`, `Answer`, `Left`, `Kept`, `Narrows`, the sample set `Plants`), `machine.ergo` (`Quiz`, `Settled`, `Signal`, `Picked`, the typed refusals, `Settle`, `Begin`, `Step`), `sets.ergo` (`Sets`, the contract of a store of question sets, with two laws), `laws.ergo` (the `Picker` machine and its laws), `tests.ergo`. It is pure. |
| `memory/` | `Shelf`, the in-memory provider of `quiz.Sets`: `plants` and `teas`. |
| `script/` | The line protocol: `Parse` (fails `BadLine`), `Show` and `Run`, the only function with capabilities (`Console`, `io.Stdin`, `quiz.Sets`). |
| `cmd/quiz/` | The binary. `main.ergo` binds `let Live = context { quiz.Sets: memory.Shelf }` and the host's standard input, and is the only place that chooses them. |

## What it shows

- **A machine with an input (V4).** `type Picker machine[Quiz, Signal] { initial: Begin, step: Step }`, with `func Begin(set Set) Quiz`: the question set is the machine's input. `script.Run` starts it with `machines.Start(quiz.Picker{}, set)`, and the run test runs `machines.Run(machines.Start(Picker{}, Plants()), signals)`.
- **Eventless steps as a pure settle (M48).** `Quiz = Calculating | Asking | Showing`. `Calculating` only decides what comes next. `Settle` runs those steps until the state is settled, and every `Enter` of the step goes through it, so a signal never sees `Calculating`. The type says so too: `Settle` returns `Settled = Asking | Showing`, a second union over two of the same variants, and every `match` on it has two arms. Each skip moves the next question on, so `Settle` ends after at most one step per question. effect-machine instead counts its eventless steps and stops at 100.
- **History in the state.** The answers are a stack in the state. `Back` pops one and calculates again from its question, so `Back` needs no saved earlier states.
- **Ordered guards and typed refusals.** `Step` fails `NoSuchChoice | NoMatch | NothingToUndo | NoQuestion | NotShowing | NoSuchItem`. `script.turn` catches each by name and tells it.
- **Exhaustive `match`.** Every rule names every variant of `Quiz`, `Settled`, `Signal` and `Decision`. The only `case _` arms are over a script word or a set name, which are strings.
- **Laws.** `Picker` declares six laws after its entries (MU6, `laws.ergo`), run by `ergo test`. `law Settles never reaches Calculating` states the settle rule over every run: no start and no signal leaves the picker in the eventless state. The five laws with a body run over question sets and walks drawn from integers (`setOf`, `walk`), so that answers meet tags and questions repeat. All six are **sampled**, none is proved: a question set holds strings and integers, which cannot be listed, so `ergo test` runs 100 generated runs or cases of each (types.md Laws 10), and the reachability law starts from generated sets.
  - `Settles`: `ok law Picker.Settles (100 runs)`.
  - `SettleIsStable`: `Settle` asks the first question that narrows and skips only ones that do not.
  - `BackUndoes`
  - `ResultsMatchAnswers`: the items left are exactly the ones that every answer keeps.
  - `NoDeadEnd`: there is always an item left, and every choice that is taken leaves one.
  - `PickedIsShown`

  The contract `quiz.Sets` has two member laws, `EverySetPlays` and `UnlistedIsNoSuchSet`.
- **Tests.** The laws are the tests. `quiz/tests.ergo` has one run of the machine, `PlantsRun`. `memory/tests.ergo` calls the two `Sets` laws against `Shelf` (G-ma-2).

## Limits found

- **No eventless transition (M48).** The pattern above stands in for one. It costs a second union (`Settled`) and an `AsQuiz` conversion back. `Step` also settles its input itself, so that it stays total over `Quiz`.
- **A union is not assignable to a union that holds its variants.** A `Settled` value is not a `Quiz`, so `AsQuiz` matches it to rebuild it. Clause 4 of Assignability admits a variant into its union, not a union into a wider one (inferred from the checker's S108 on the assignment; not filed as a gap, because it may be the intended rule).
- **`==` on a state that holds slices (M29).** The laws and the test compare states through `Key`.
- **A reachability law starts from the input type's generator, not from a law's own builder (MU6b).** A generated `Set` rarely has a question that narrows, so `law AnswerShows path [Asking, Showing]` found no witness in 100 runs (measured, seeds 1 to 5) and is not declared; the laws with a body build their sets with `setOf` instead.
- **The examples runner runs a package's laws only when the package declares a `test` (G-ma-1)**, so `quiz/` keeps `PlantsRun`.
- **A law of an imported operation type does not run against another package's provider (G-ma-2)**, so `memory/tests.ergo` calls the laws' functions.
- **`Run` needs a first state before the set is read.** `let mut q quiz.Quiz` needs a value, so it starts as `Begin(Plants())` and is replaced by the state that the script's set gives. A `let` with no value is allowed only for a type with a default, and a union has none.

## Run it

```sh
go build -o bin/ergo ./cmd/ergo
./bin/ergo run examples/quiz/cmd/quiz < examples/quiz/cases/plants/stdin
./bin/ergo test examples/quiz/quiz
go run ./scripts/examples quiz
```

## Cases

- `plants`: a pick before the results, a skipped question, `back` twice, the results, `restart`, a choice that keeps nothing, a choice that does not exist, a second skipped question, a result that does not exist, a pick, and a line after the pick that is not read.
- `teas`: the first question is skipped at the start, a line the script cannot read, a choice that keeps nothing, and the input ends before a pick (status 2).
- `noset`: a set the store does not hold (status 1).
