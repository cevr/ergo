# Example programs

Each folder of `examples/` is one Ergo program with recorded cases. The runner `scripts/examples` builds each program with the checkout's `bin/ergo` and compares what the program prints with its cases: stdout, stderr and the exit status, byte for byte. The gate step `examples (default)` runs the programs tagged `default`.

```sh
go build -o bin/ergo ./cmd/ergo
go run ./scripts/examples                      # the default programs
go run ./scripts/examples PROGRAM              # one program folder, whatever its tags
go run ./scripts/examples -tags slow,net       # the programs with either tag
```

One folder is not a program: `editions` (edition fixtures that Go tests pin). Any other folder that is not a program is an error. Go interop is shown by `gointerop`, an `import go` program.

## Layout

A program folder holds:

- `main.ergo`: the program. The runner builds it with `ergo build <dir>/main.ergo -o <temp>/<name>`, outside the checkout.
- other `.ergo` files, if any. `ergo fmt --check` checks every top-level `.ergo` file before the build.
- `README.md`: what the program shows and how it works.
- `tags`: one line of words from `default slow net cc tty gui ts`. `default` stands alone. The other tags keep a program out of the default gate: `slow` (a case runs longer than the budget), `net` (it uses the network), `cc` (it needs a C compiler), `tty` (it needs a terminal), `gui` (it opens a window) and `ts` (it has a TypeScript side; see "TypeScript leg"). The `tty` and `gui` legs are not built yet, so selecting such a program is an error.
- `cases/`: one folder per case, at least one.
- `origin` and `record.sh`, both or neither: a port names its original program in `origin`, and `record.sh` runs that original (see "Record leg").
- `expected-skips`, optional: the `skip law …` lines that the program's `ergo test` may print, one per line exactly as printed, with `#` lines for the reason. The test leg fails on any other skip line (fs4).
- `testdata/`, if the program reads fixture files.

A program of packages (196b; the Go layout of `AGENTS.md`) holds `ergo.mod` in place of `main.ergo`:

- `ergo.mod`: `module NAME`, the module root ([grammar](../docs/spec/grammar.md#source-files) rule 1).
- `cmd/<program>/main.ergo`: the binary's package, named after the program folder. `cmd/` holds nothing else. The runner builds it with `ergo build <dir>/cmd/<program> -o <temp>/<program>`.
- package folders: any other folder except `cases/` and `testdata/`. A package folder holds `.ergo` files and package folders only.
- top-level `.ergo` files, if any: the module root's package.
- `README.md`, `tags`, `cases/`, and optionally `origin` with `record.sh`, `expected-skips` and `testdata/`, as above.

`ergo fmt --check` checks every `.ergo` file of its packages, and the test leg runs `ergo test <package folder>` for every package folder, printing `ok <program>: test <folder>`. A package's test build runs the laws of every operation type it sees against every provider it sees, wherever each is declared, in the least package that sees them all (law-xpkg): the laws `kv.Store` declares run in package `memory` against `memory.Table`, and the std `fs.Writes` laws run there against `memory.Full`. A provider is built by its `New<P>` constructor when its package declares one, else generated.

A domain package states each contract it needs as an operation type with laws (`AGENTS.md`). So in a program of packages, an operation type declared outside `cmd/` with no member law is a layout error: `examples/<name>: <file>:<line>: operation type T has no law; …`. The runner reads each package folder with the checker for this check, as `ergo check DIR` does, and takes the operation types and their member laws from the checked package; a package the checker refuses is a layout error that names the diagnostic. `go run ./scripts/examples -lawless` reports every operation type with no law under `examples/`, in any program or folder, runs nothing, and exits 0.

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

Each case runs in a fresh empty directory, which is also its `HOME`. Its environment is exactly `PATH`, `HOME` and its `env` lines; nothing else of the runner's environment reaches it. The program runs in its own process group, and the runner stops that group when the program ends, after `-timeout` (default 30 s), when the program writes more than 1 MiB (1048576 bytes) to stdout or to stderr, on a panic of the runner, and on SIGINT, SIGTERM or SIGHUP to the runner, so no case leaves a process behind. Output past the cap fails the case with `FAIL <program>/<case>: stdout over 1048576 bytes`, `stderr over 1048576 bytes`, or both, stdout first and joined by `; `. The verdict names each stream that passed the cap before the runner's stop took effect, so when a program writes past the cap on both streams, the verdict can name either stream or both (`TestOutputCap`, `TestOutputCapLateStopNamesBothStreams`). This verdict replaces every other verdict and prints no diff; the runner keeps output in memory only and writes none of it to disk. On a signal the runner also removes its scratch and the case's directory, prints `examples: stopped by <signal>` and exits 128 plus the signal number (130, 143, 129). A case of a `default` program that runs longer than `-budget` (default 5 s) fails: tag the program `slow`.

A case passes when stdout, stderr and the status all equal the recorded ones. A failure names each stream that differs and shows a unified diff (`--- want`, `+++ got`). The runner prints `ok <program>/<case> (<time>)` or `FAIL <program>/<case>: <what>`, and exits 0 when every case passes, 1 when a program or a case fails, and 2 on a usage or layout error.

## TypeScript leg

A program of packages tagged `ts` holds a `ts/` folder, its TypeScript side, which imports the program's `.ergo` contracts through `tools/ts` (the content mapper for `tsc`, the Bun loader at run time; [tools/ts](../tools/ts/README.md)). The tag and the folder go together, and `ts/` holds no `.ergo` file. `go run ./scripts/examples -tags ts` runs these programs; the default gate does not.

The leg needs Bun on `PATH` and a Bun cache warmed from `tools/ts/bun.lock`. Once per run, it installs `tools/ts`'s dependencies in the runner's scratch, outside the checkout, with `bun install --frozen-lockfile --offline --ignore-scripts`, and puts `ergo-ts` beside them. A host without Bun, or a cache without the locked packages, fails the program (`FAIL <program>: ts: …`); its cases are never skipped as passed.

A case of a ts program runs `bun` with the case's `args` (so `--preload ergo-ts/bun ts/main.ts` runs a TypeScript file, and `../node_modules/typescript/bin/tsc …` runs tsc), in a fresh copy of the program folder under that install, which `setup.sh` may edit (it runs in the copy too). `HOME` is still the case's empty directory. The environment adds the checkout's `ergo` first on `PATH` and as `ERGO`, and the built program as `ERGO_BIN`: the case's TypeScript starts the Ergo server itself, in its own process group, so the runner's stop of the group stops both. Servers bind `127.0.0.1:0` and print `listening ADDR` first.

## Test leg

The runner tests every package, as `go test ./...` does, whether or not it declares a test or a law: the compiler decides what its test build runs, which may be a std law against a provider it declares (fu6b, G-ma-1). For a program of one folder the runner runs `ergo test <dir>/main.ergo` after the build and prints `ok <program>: test (<time>)`, or `FAIL <program>: test` with the command's output. A failing test fails the program, and its cases still run. A test leg is never skipped: any reply other than a pass is a failure. A reply over the cap fails as `FAIL <program>: test: stdout over 1048576 bytes`, with the stream names of the cap rule above, and with no output printed.

## Record leg

A port's goldens come from the original program, never from the port. `go run ./scripts/examples -record PROGRAM` runs `sh <program>/record.sh ARGS...` for each case, as the case leg runs a program (the same directory, environment, input and process group), and writes `expected.out`, `expected.err` (only when stderr is not empty) and `expected.status` (only when the status is not 0). It removes a stale `expected.err` or `expected.status`. An original that writes over the cap fails and records nothing. `record.sh` finds its original from its own path (`$0`). `-record -check` runs the original the same way and compares its output with the goldens, and writes nothing. A program without `record.sh` cannot be recorded.

An original program has no record leg: its goldens come from an independent model or are written by hand, and a review checks them.
