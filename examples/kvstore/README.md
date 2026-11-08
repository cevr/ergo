# kvstore

A key-value store on the command line. It answers one command from its arguments, or a session of commands from standard input, one per line: `put KEY VALUE`, `get KEY`, `del KEY`, `keys`, `dump` and `compact`. `-store file` (the default) keeps the entries in `DIR/kv.log` (`-dir`, default `data`); `-store memory` keeps them for one run.

Tag: `default`. It is an original program, not a port. Its goldens are written by hand from the rules below, and the program's own tests check the same rules over a memory disk.

## Layout

The program is a module of packages (`ergo.mod`: `module kvstore`), in the Go layout:

| folder | what it holds |
| --- | --- |
| `kv/` | The domain. `store.ergo` (`Store`, the contract, with six laws, and the failures `Missing` and `Broken`), `checks.ergo` (one function per law, which each provider's tests call), `entries.ergo` (`Entry`, `Snapshot`, `Lookup`), `command.ergo` (`Command`, `Parse`, the failures `Unknown` and `Usage`), `session.ergo` (`Exec`, `Answer`, `Session`, `One`), `laws.ergo` (the laws of `Parse`), `tests.ergo`. |
| `memory/` | `Table`, the in-memory `kv.Store`; `Disk`, an in-memory `fs.Files` and `fs.Writes`; `Full`, an `fs.Writes` of a full disk. |
| `file/` | `Log`, the `kv.Store` in a log file: `record.ergo` (`Record`, `Encode`, `Decode`, `Replay`, `Compacted`), `log.ergo` (`Log`, `Open`), `laws.ergo`, `tests.ergo`. |
| `cmd/kvstore/` | The binary. `main.ergo` reads the command line with std `flag`, chooses the store, binds the host's file system and streams, and is the only place that chooses a provider. |

## What it shows

- **One contract, two providers, one set of checks.** `kv.Store` states its laws once: `GetAfterPut`, `PutOverwrites`, `DeleteRemoves`, `PutKeepsOthers`, `KeysAreSortedAndDistinct` and `CompactKeepsContents`. `memory.Table` and `file.Log` both provide it. Their tests run the same check functions (`kv.GetAfterPutHolds` and the others) in a context that binds each provider, so the two stores keep one contract by construction, not by two copies of the tests.
- **A model test.** `file`'s `LogAgreesWithTable` generates scripts of puts, deletes and compactions (a `[]Step` of a union, which the property runner generates) and checks that a `Log` answers each script as the `Table` does.
- **Crash-safe writes.** The log is written by `fs.Writes.WriteFile`, which writes a new file beside the log and renames it over the log, so a crash leaves the old log or the new one, never a part. The law `FailedWriteKeepsTheStore` binds a `memory.Full` disk: a put fails `Broken`, and every entry keeps its value.
- **A log and its compaction.** Each put and delete is one line, `put\t"key"\t"value"` or `del\t"key"`, quoted by `strconv.Quote`, so a value with a tab or a line feed is one line (laws `RoundTrip` and `OneLine`). `Compact` rewrites the log as one put per key; `CompactedReplaysTheSame` and the store law `CompactKeepsContents` state that it loses nothing.
- **Typed failures, matched exhaustively.** `Missing` is an answer (`missing KEY`); `Broken` ends the run with status 1 and the log's line number when a line is not a record. `Parse` fails `Unknown | Usage`, and `Answer` tells each. `Command` and `Record` are unions, and every `match` on them names each variant.
- **Providers that hold the host.** A provider's operation cannot need a capability that the contract's operations do not write (rows.md, Context 2). So `file.Log` holds the `fs.Files` and `fs.Writes` values that `Open`'s caller gives it, as a Go type holds an `fs.FS`. `main` gives it `fs.NewHost()`, and the tests give it a `memory.Disk`: no test touches the host disk.
- **Shared state, as in Go.** A `Table` and a `Disk` keep their entries in a `Map`, which every copy shares, so a provider bound in a context keeps its entries from one call to the next (`ACopySharesTheEntries`).

## Run it

```sh
go build -o bin/ergo ./cmd/ergo
./bin/ergo run examples/kvstore/cmd/kvstore put greeting "hello, world"
./bin/ergo run examples/kvstore/cmd/kvstore get greeting      # prints: hello, world
printf 'put a 1\ndump\n' | ./bin/ergo run examples/kvstore/cmd/kvstore -store memory
./bin/ergo test examples/kvstore/kv
./bin/ergo test examples/kvstore/memory
./bin/ergo test examples/kvstore/file
./bin/ergo test examples/kvstore/cmd/kvstore
go run ./scripts/examples kvstore
```

## Cases

`session` (every command and each failure of a line, on the memory store), `file` (a session on the file store: overwrite, delete, compaction), `persisted` (a log that `setup.sh` writes is replayed: an overwrite, a delete and a value of two lines), `corrupt` (a line that is not a record: status 1, and the line on standard error) and `usage` (an unknown store: status 2).

## Limits found

Each limit is recorded with a repro in the lane's gap file (`gaps-ex-c.md`, run dir).

- **G-ex-c-4: `fs.Writes` has no rename, remove or append.** The program cannot write its own temp file and rename it; it relies on `WriteFile`'s own temp-and-rename, which is crash-safe for one file. Each put reads and writes the whole log, so a put costs O(log size), and a temp file that a crash leaves is never removed.
- **G-ex-c-5: a member law cannot share its check.** Laws are checked, not run (#162), and a member law cannot call a function that needs its own type, so each law is written twice: in `kv.Store` and as a `…Holds` function in `kv/checks.ergo`.
- **G-ex-c-3: a function cannot return a value of an error type.** A tail of an error type is a failure, so the helper `broken` that makes an `os.Exit` from a `kv.Broken` is written `fails os.Exit` and called as `return broken(b)`; the diagnostics for the first spelling point at its callers.
- **G-ex-c-2: a parenthesized literal in an `if` header lowers to Go that does not compile.** `sameMaps` binds `Some[string]{…}` with `let` first.
- **G-ex-c-6: `flag.Choice` reads a string.** The match on the store needs `case _` for `file`.
- **G-ex-c-1: diagnostics name a type of a package folder as `main.T`.**
- Syntax friction, no gap: `try` stands only at the start of an expression, so `a && try f()` is written `a && (try f())`, and a `catch` ends its expression, so `try f() catch { … } == v` is written through a helper (`kv.Lookup`).
