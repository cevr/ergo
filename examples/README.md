# Example programs

Each folder of `examples/` is one Ergo program with recorded cases. The runner `scripts/examples` builds each program with the checkout's `bin/ergo` and compares what the program prints with its cases: stdout, stderr and the exit status, byte for byte. The gate step `examples (default)` runs the programs tagged `default`.

```sh
go build -o bin/ergo ./cmd/ergo
go run ./scripts/examples                      # the default programs
go run ./scripts/examples PROGRAM              # one program folder, whatever its tags
go run ./scripts/examples -tags slow,net       # the programs with either tag
```

Two folders are not programs: `editions` (edition fixtures that Go tests pin) and `go-interop` (a Go program that the Go steps build). Any other folder that is not a program is an error.

## Layout

A program folder holds:

- `main.ergo`: the program. The runner builds it with `ergo build <dir>/main.ergo -o <temp>/<name>`, outside the checkout.
- other `.ergo` files, if any. `ergo fmt --check` checks every top-level `.ergo` file before the build.
- `README.md`: what the program shows and how it works.
- `tags`: one line of words from `default slow net cc tty gui`. `default` stands alone. The other tags keep a program out of the default gate: `slow` (a case runs longer than the budget), `net` (it uses the network), `cc` (it needs a C compiler), `tty` (it needs a terminal) and `gui` (it opens a window). The `tty` and `gui` legs are not built yet, so selecting such a program is an error.
- `cases/`: one folder per case, at least one.
- `origin` and `record.sh`, both or neither: a port names its original program in `origin`, and `record.sh` runs that original (see "Record leg").
- `testdata/`, if the program reads fixture files.

Nothing else. The runner checks the layout of every folder before any case runs, whatever the selection. A layout error prints `examples/<name>: <reason>` and exits 2.

## Cases

A case folder holds `expected.out` and may hold these files. Any other file is a layout error, so a misspelt golden such as `expected.stdout` is found.

| file | meaning |
| --- | --- |
| `args` | the arguments, one per line. The final newline ends the last line, and an empty line is an empty argument. No file means no arguments. |
| `stdin` | the standard input. No file means empty input. |
| `env` | `KEY=VALUE` lines that the case adds to its environment. |
| `setup.sh` | runs as `sh <case dir>/setup.sh <case dir>` in the case's directory before the program, for example to copy a fixture. A nonzero exit fails the case. |
| `expected.out` | the stdout. |
| `expected.err` | the stderr. No file means empty. |
| `expected.status` | the exit status, in decimal (the final newline is optional). No file means 0. |

Each case runs in a fresh empty directory, which is also its `HOME`. Its environment is exactly `PATH`, `HOME` and its `env` lines; nothing else of the runner's environment reaches it. The program runs in its own process group, and the runner stops that group when the program ends or after `-timeout` (default 30 s), so no case leaves a process behind. A case of a `default` program that runs longer than `-budget` (default 5 s) fails: tag the program `slow`.

A case passes when stdout, stderr and the status all equal the recorded ones. A failure names each stream that differs and shows a unified diff (`--- want`, `+++ got`). The runner prints `ok <program>/<case> (<time>)` or `FAIL <program>/<case>: <what>`, and exits 0 when every case passes, 1 when a program or a case fails, and 2 on a usage or layout error.

## Test leg

A program can declare its own tests: a top-level `.ergo` file with a line that starts with `test` and a name. For such a program the runner runs `ergo test <dir>/main.ergo` after the build and prints `ok <program>: test (<time>)`, or `FAIL <program>: test` with the command's output. A failing test fails the program, and its cases still run. A test leg is never skipped: any reply other than a pass is a failure.

## Record leg

A port's goldens come from the original program, never from the port. `go run ./scripts/examples -record PROGRAM` runs `sh <program>/record.sh ARGS...` for each case, as the case leg runs a program (the same directory, environment, input and process group), and writes `expected.out`, `expected.err` (only when stderr is not empty) and `expected.status` (only when the status is not 0). It removes a stale `expected.err` or `expected.status`. `record.sh` finds its original from its own path (`$0`). `-record -check` runs the original the same way and compares its output with the goldens, and writes nothing. A program without `record.sh` cannot be recorded.

An original program has no record leg: its goldens come from an independent model or are written by hand, and a review checks them.
