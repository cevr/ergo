# kvstore

A key-value store on the command line. It answers one command from its arguments, or a session of commands from standard input, one per line: `put KEY VALUE`, `get KEY`, `del KEY`, `keys`, `dump` and `compact`. `-store file` (the default) keeps the entries in `DIR/kv.log` (`-dir`, default `data`); `-store memory` keeps them for one run.

Tag: `default`. It is an original program, not a port. Its goldens are written by hand from the rules below, and the program's own tests check the same rules over a memory disk.

## Layout

The program is a module of packages (`ergo.mod`: `module kvstore`), in the Go layout:

| folder | what it holds |
| --- | --- |
| `kv/` | The domain. `store.ergo` (`Store`, the contract, with six laws, and the failures `Missing` and `Broken`), `checks.ergo` (one function per law, which each provider's tests call), `entries.ergo` (`Entry`, `Snapshot`, `Lookup`), `command.ergo` (`Command`, `Parse`, the failures `Unknown` and `Usage`), `session.ergo` (`Exec`, `Answer`, `Session`, `One`), `laws.ergo` (the laws of `Parse`), `tests.ergo`. |
| `memory/` | `Table`, the in-memory `kv.Store` (`table.ergo`); `Full`, an `fs.Writes` of a full disk over an `fstest.Memory` (`full.ergo`). The tests' files are std's `fstest.Memory`. |
| `file/` | `Log`, the `kv.Store` in a log file: `record.ergo` (`Record`, `Encode`, `Decode`, `Replay`, `Compacted`), `log.ergo` (`Log`, `Open`), `laws.ergo`, `tests.ergo`. |
| `cmd/kvstore/` | The binary. `main.ergo` reads the command line with std `flag`, chooses the store, binds the host's file system and streams, and is the only place that chooses a provider. |

## What it shows

- **One contract, two providers, one set of checks.** `kv.Store` states its laws once: `GetAfterPut`, `PutOverwrites`, `DeleteRemoves`, `PutKeepsOthers`, `KeysAreSortedAndDistinct` and `CompactKeepsContents`. `memory.Table` and `file.Log` both provide it, and `ergo test` runs every law on each, so the two stores keep one contract by construction, not by two copies of the tests.
- **A model test.** `file`'s `LogAgreesWithTable` generates scripts of puts, deletes and compactions (a `[]Step` of a union, which the property runner generates) and checks that a `Log` answers each script as the `Table` does.
- **Appends and crash-safe compaction.** A put or a delete is one `fs.Writes.AppendFile` of its line, so it costs one line, not the log; a crash inside it can leave a part of a line, which reads as a corrupt line (the store is broken and says where, never silently wrong). `Compact` writes by `fs.Writes.WriteFile`, which writes `kv.log.tmpN` beside the log and renames it over the log, so a crash leaves the old log or the new one, never a part; `Open` removes a `kv.log.tmpN` that a crash left (`OpenRemovesStaleTempFiles`). The law `FailedWriteKeepsTheStore` binds a `memory.Full` disk: a put fails `Broken` with the host's reason, and every entry keeps its value.
- **A log and its compaction.** Each put and delete is one line, `put\t"key"\t"value"` or `del\t"key"`, quoted by `strconv.Quote`, so a value with a tab or a line feed is one line (laws `RoundTrip` and `OneLine`). `Compact` rewrites the log as one put per key; `CompactedReplaysTheSame` and the store law `CompactKeepsContents` state that it loses nothing.
- **Typed failures, matched exhaustively.** `Missing` is an answer (`missing KEY`); `Broken` ends the run with status 1 and the log's line number when a line is not a record. `Parse` fails `Unknown | Usage`, and `Answer` tells each. `Command` and `Record` are unions, and every `match` on them names each variant.
- **Providers that hold the host.** A provider's operation cannot need a capability that the contract's operations do not write (rows.md, Context 2). So `file.Log` holds the `fs.Files` and `fs.Writes` values that `Open`'s caller gives it, as a Go type holds an `fs.FS`. `main` gives it `fs.NewHost()`, and the tests give it an `fstest.Memory`: no test touches the host disk.
- **Shared state in a `sync.Ref`.** A `Map` is a value, so a `Table` keeps its entries in a `sync.Ref`, which every copy shares: a provider bound in a context keeps its entries from one call to the next (`ACopySharesTheEntries`), and `Delete` is one `sync.Modify`. `Put`'s `Update` and `Delete`'s `sync.Modify` take their function as a trailing function literal, `sync.Modify(t.entries) func(entries Map[string, string]) (bool, Map[string, string]) { … }` (grammar.md, Expressions 9), which a `return` inside may leave early. `NewTable` is the constructor `ergo test` builds a provider with for each case of a law.

## Run it

```sh
go build -o bin/ergo ./cmd/ergo
./bin/ergo run examples/kvstore/cmd/kvstore -- put greeting "hello, world"
./bin/ergo run examples/kvstore/cmd/kvstore -- get greeting      # prints: hello, world
printf 'put a 1\ndump\n' | ./bin/ergo run examples/kvstore/cmd/kvstore -- -store memory
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

Closed in batch 12: G-ex-c-4 (`fs.Writes` now appends, renames and removes: a put is one append and `Open` removes stale temp files), G-ex-c-3 (`broken` returns an `os.Exit`), G-ex-c-2 (`sameMaps` gone: maps compare with `==`).

- **G-ex-c-5: a member law cannot share its check.** `ergo test` runs `Store`'s laws in `memory` against `memory.Table` and the `fs.Writes` laws against `memory.Full` (`NewFull`; G-exup-1 closed in batch 13), and, since fu7a, against `file.Log` (`NewLog`, over the case's own `fstest.Memory`). A member law cannot call a function that needs its own type, so each law is also a `…Holds` function in `kv/checks.ergo`, which `memory`'s tests call.
- **G-ex-c-6: `flag.Choice` reads a string.** The match on the store needs `case _` for `file`.
- **G-ex-c-1** (closed by batch-12 K4, rechecked in L9): diagnostics name a package's own types as its source writes them (measured: the repro prints `{Broken} escapes function Use`; `TestDiagnosticTypeNamesResolve` holds the rule).
- Syntax friction, no gap: `try` stands only at the start of an expression, so `a && try f()` is written `a && (try f())`, and a `catch` ends its expression, so `try f() catch { … } == v` is written through a helper (`kv.Lookup`).
