# confparse

A configuration reader for a reverse proxy, from a subset of TOML into typed records, with environment overrides and failures that say where they are.

```text
$ confparse check proxy.toml
edge: listen 0.0.0.0:8443, log warn
upstream eu-1 10.0.0.1:9000 weight 5
upstream eu-2 10.0.0.2:9000 weight 1
upstream spare 10.0.9.9:9000 weight 1 backup
$ CONFPARSE_LISTEN_PORT=https confparse check proxy.toml
proxy.toml: $CONFPARSE_LISTEN_PORT: port must be an integer, not "https"
$ confparse check typo.toml
typo.toml:6:1: unknown key adress in [upstream]
```

The commands: `check FILE` decodes the file, with the `CONFPARSE_` variables over it, and prints what it says; `get FILE KEY` prints one value as the file writes it (`name`, `listen.port`, `upstream.2.backup`); `fmt FILE` prints the file in canonical form. A failure ends with status 1, a usage error with status 2.

The TOML subset: `#` comments, `[table]` and `[[array of tables]]` headers (a name may hold dots, which are part of the name), bare keys, basic strings with the escapes `\"`, `\\`, `\n`, `\t`, `\r`, literal strings, decimal integers with `_` between digits and an optional sign, `true` and `false`, and arrays on one line, nested, with an optional comma at the end. It has no floats, dates, inline tables, multi-line strings or arrays, quoted or dotted keys, and no `\u` escapes.

The configuration (`config/config.ergo`): `name` (required); `[listen]` with `host` (default `127.0.0.1`) and `port` (required, 1 to 65535); `[log]` with `level` (`debug`, `info`, `warn` or `error`; default `info`); and any number of `[[upstream]]` with `name` (required, distinct), `addr` (required), `weight` (1 to 100, default 1) and `backup` (default false). Any other key or table is refused. `CONFPARSE_NAME`, `CONFPARSE_LISTEN_HOST`, `CONFPARSE_LISTEN_PORT` and `CONFPARSE_LOG_LEVEL` override the file.

Tag: `default`. It is an original program, not a port. Its goldens were made by running the program and then checked line by line by hand against the rules above.

## Layout

The program is a module of packages (`ergo.mod`: `module confparse`), in the Go layout:

| folder | what it holds |
| --- | --- |
| `toml/` | The syntax. `doc.ergo` (`Pos`, the union `Value`, `Item`, `Entry`, `Table`, `Doc`, `Lookup`, `Named`, `Kind`), `parse.ergo` (`Parse` over a `*cursor` borrow; failures `Unexpected`, `Unterminated`, `BadEscape`, `TooBig`, `Duplicate`, each with a line and column), `problem.ergo` (the union `Problem`, the named row of `Parse`, `PosOf`, `Describe`), `print.ergo` (`Print`, `PrintValue`, `Quote`, `Same`, `SameDoc`: equality wherever the parts were read), `gen.ergo`, `laws.ergo`, `tests.ergo`. |
| `config/` | The domain. `config.ergo` (`Config`, `Listen`, the union `Level`, `Upstream`), `decode.ergo` (the union `Origin` of a place in the file or a variable; `Decode` with failures `MissingKey`, `WrongType`, `OutOfRange`, `UnknownKey`, `UnknownTable`, `BadChoice`, `Clash`; the union `Found` of what a key has), `encode.ergo` (`Encode`, `Valid`), `problem.ergo` (the union `Problem`, the named row of `Decode`, `Where`, `Describe`, `Place`), `gen.ergo`, `laws.ergo`, `tests.ergo`. |
| `cmd/confparse/` | The binary. `main.ergo` reads the command line as the union `Cmd` with `flag.ParseCommand`, reads the file and the variables, and binds the host's streams, files and process. Its tests run `run` over an `fstest.Memory`, a Process provider with set variables and a Recorder. |

## What it shows

- **Typed failures with a place, told by exhaustive matches.** `toml.Parse` fails `toml.Problem`, a named row of five records, and `config.Decode` fails `config.Problem`, seven, and the binary catches each row whole with `case err:`, as a value of its package's union `Problem`. `Where` gives each decode failure's `Origin`, itself a union of `InFile{At}` and `InEnv{Name}`, so a message points at a line and column or at the variable that was wrong. No `case _` anywhere over a failure.
- **Refined types instead of range checks.** `type Port int where 1 <= it && it <= 65535` and `type Weight int where 1 <= it && it <= 100` derive `ParsePort` and `ParseWeight`, the only ways a non-constant value enters them, so a `Config` never holds port 0 and `Valid` checks no range. `Decode` turns their `Unsatisfied` into `OutOfRange`, which carries the type's name and its where. The derived laws `PortWhere` and `WeightWhere` run with the others; `Weight` has 100 values, so its law is proved over every one.
- **Decoding into records by type.** `Decode` reads each key through `find`, whose result is the union `Found` (`FromFile`, `FromEnv`, `Absent`), so a variable wins over the file in one place, and a default is an `Option` argument, not a zero value.
- **Laws across two packages.** `toml.PrintParses` (a printed document reads back as itself), `toml.PrintIsCanonical`, `toml.ProblemPointsIntoTheText`, `config.DecodeInvertsEncode` (encode, print, parse, decode is the identity on valid configurations), `config.EnvWins` and `config.FailuresPointIntoTheText`. `ergo test` runs them. A generated `Doc` or `Config` is seldom Printable or Valid, so each law also takes a `seed` and checks `toml.Sample(seed)` or `config.Sample(seed)`, which `Gen` builds from `rand.Seeded(seed)`. Mutants of the printer and the encoder are killed (`confparse-mutants.json` of the ex-upgrade unit, 2 of 2).
- **A command line as a union.** `Cmd = Check | Get | Fmt`, built by `flag.ParseCommand`; a required argument's `None` arm fails `flag.Missing` rather than invent a value.
- **Capabilities as values.** The variables come from `os.Process.Getenv`; the tests bind their own `Process` and `Files` providers.

## Run it

```sh
go build -o bin/ergo ./cmd/ergo
./bin/ergo run examples/confparse/cmd/confparse -- -h
./bin/ergo test examples/confparse/config
go run ./scripts/examples confparse
```

## Cases

`check` (a whole file with comments, both string forms, an integer with `_`, three upstreams), `env` (three variables; the upstream one is not read), `badenv` (a variable that is not an integer: status 1), `typo` (a misspelt key in an upstream), `syntax` (a bad escape inside an array: `fmt` fails at its column), `get` (`upstream.2.backup`), `fmt`, `usage` (`get` with no key: status 2) and `help`.

## Limits found

Each gap has its repro in the run dir's `examples-wave/gaps-ex-a.md`.

- **G-ex-a-16 (missing language feature).** No derived decoding: each record's keys, kinds and defaults (the ranges are refined types now, L6) are written by hand in `Decode`, and the list of known keys for `UnknownKey` repeats them. Go decodes by struct tags and reflection; Effect by a `Schema`.
- **Not a gap, found on the way.** A `match` or `if` whose branches are `Some[T]{…}` and `None` takes the first branch's type, so `Value` writes the type of the tuple it binds (`let found (Option[toml.Table], string) = match …`); the rule is types.md's, and the annotation is the fix it names.

Closed in batch 12: G-ex-a-15 (K1: `missing(src, key)` builds the `MissingKey`, and `return missing(src, key)` checks; `tableAt` is gone), named rows for `Parse` and `Decode` (L2), `config.Same` (W3: `==`), the test `Files` provider (`fstest.Memory`), `ergo run DIR -- ARGS` (T1), and the property tests that restated the laws (V1: the laws run, with the steering moved into them). `toml.SameDoc` stays: it compares documents wherever their parts were read, which `==` (positions included) does not.
