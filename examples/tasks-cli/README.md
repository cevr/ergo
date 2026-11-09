# tasks-cli

A small linear-style task CLI. It keeps its tasks in a text file (`$TASKS_FILE`, default `tasks.txt`):

```sh
tasks-cli create --priority urgent fix the build     # created #1   urgent  open    fix the build
tasks-cli list [--state open|closed|all] [--min low|medium|high|urgent] [--json]
tasks-cli close [--yes] [--json] ID                  # asks "close #1 fix the build? [y/N]" first
tasks-cli completion bash|zsh|fish                   # a completion script
```

A listing puts the most urgent task first. A usage error exits 2 (help exits 0), as Go's `flag` does; a task that does not exist, a second close, a declined prompt or a broken file exits 1 with `tasks-cli: …` on standard error.

Tag: `default`. It is an original program, not a port.

## Layout

The program is a module of packages (`ergo.mod`: `module taskscli`; `module tasks` is S108, since a std import always names std `tasks`), in the Go layout:

| folder | what it holds |
| --- | --- |
| `tasks/` | The domain. `task.ergo` (`Task`, the unions `Priority` and `State`, their names), `board.ergo` (`Board`, the store contract with four laws, and the failures `NotFound`, `AlreadyClosed`, `Broken`), `checks.ergo` (one function per `Board` law, which each provider's tests call), `view.ergo` (`Which`, `View`, `Before`), `laws.ergo` (the listing laws), `tests.ergo`. |
| `memory/` | `Board`, the in-memory `tasks.Board`, its tasks in a `sync.Ref`. The file Board's tests use `fstest.Memory`. |
| `file/` | `Board`, the `tasks.Board` in a text file: `line.ergo` (`Encode`, `Decode`, the failure `Corrupt`), `board.ergo`, `laws.ergo` (`RoundTrip`, `OneLine`), `tests.ergo`. |
| `cli/` | The command line: `command.ergo` (`Cmd`, a union, and `Commands`, parsed by std `flag.ParseCommand`), `run.ergo` (`Run`), `render.ergo` (text and JSON), `confirm.ergo` (the `[y/N]` prompt), `completion.ergo` (bash, zsh and fish scripts), `laws.ergo`, `tests.ergo`. |
| `cmd/tasks-cli/` | The binary: it binds the file `Board` on the host's file system and the host's streams, and is the only place that chooses them. |

## What it shows

- **A command line as a union.** `Cmd = List | Create | Close | Completion`: `flag.ParseCommand` builds one, and `exec` matches every variant. `ArgsRoundTrip` is its law: the command line `Args` writes for any `Cmd` parses back to it.
- **Typed failures with their exit codes in one place.** `exec` fails `NotFound | AlreadyClosed | Broken | Declined`; `Run` catches each by name and gives it its message and status, so a new failure stops the build until `Run` tells it.
- **One contract, two providers, one set of checks.** `tasks.Board` states `CreateNumbersInOrder`, `CloseClosesOnlyIt`, `CloseIsOnce` and `CloseOfNoTaskIsNotFound`; `memory.Board` and `file.Board` run the same check functions.
- **Listing laws.** `ViewShowsWhatMatches` (each matching task once, no other) and `ViewIsOrdered` (most urgent first, then oldest).
- **Completions held to the parser.** The law `CompletionsMatchTheParser` reads each command's flags back from `flag.Usage` and compares them with the completion table.
- **A JSON writer with a law.** `QuoteIsOneJSONString`: a quoted title holds no control byte and no unescaped quote.

The laws of `cli` and `file` run as they are; the `Board` laws run through `tasks/checks.ergo` in each provider's tests (G-exup-1).

## Run it

```sh
go build -o bin/ergo ./cmd/ergo
./bin/ergo run examples/tasks-cli/cmd/tasks-cli -- create write the spec
./bin/ergo run examples/tasks-cli/cmd/tasks-cli -- list --json
for p in tasks memory file cli cmd/tasks-cli; do ./bin/ergo test examples/tasks-cli/$p; done
go run ./scripts/examples tasks-cli
```

## Cases

`list` (open tasks, most urgent first), `json` (`--state all --json`), `close` (the prompt answered `y`), `declined` (answered `no`: status 1), `env` (`TASKS_FILE` names the file) and `usage` (an unknown command: status 2).

## Limits found

Each limit is recorded with a repro in the lane's gap file (`gaps-ex-c.md`, run dir).

Closed in batch 12: G-ex-c-11 (the domain's type is `tasks.Task` again: a package may declare a prelude name), G-ex-c-14 (each `Build` fails `flag.Failure`, std flag's named row), G-ex-c-10 (`let state = match …` joins), G-ex-c-9 (`cli.Quote` loops over runes and writes each with `utf8.RuneText`; `pad` is `strings.PadRight`), G-ex-c-4 for the tests (`fstest.Memory`; a close rewrites a line, so the file is still written whole).

- **G-exup-1: Board's laws run only for providers in `tasks`** (`skip law Board.…: no type of the package implements Board`), so `memory`'s and `file`'s tests run `tasks/checks.ergo`.
- **G-ex-c-12: no prompt, and `io.Stdin` cannot say whether it is a terminal.** The prompt takes the first line of `io.Stdin.Lines()`, so a piped `y` answers it too.
- **G-ex-c-13: std flag has no short names (`-j`) and no introspection.** The completion table restates each flag, held to the parser by a law.
- **G-ex-c-6, G-ex-c-7 (from kvstore and orders).** A `flag.Choice` reads a string, so each match on one needs `case _`; there is no std json, so `cli.Quote` escapes by hand.
- Friction, no gap: Ergo has no variadic parameters, so a test helper takes `[]string`.
