# jsonv

A JSON pretty-printer and a small JSON Schema validator. `jsonv fmt` prints a document indented (jq's layout), `jsonv min` prints it on one line, `jsonv check SCHEMA [FILE]` names every violation of a schema with its path (`$.tags[2]: want a string, got null`), and `jsonv infer [FILE]` prints the narrowest schema the document keeps. A missing file or `-` reads standard input.

Tag: `default`. It is an original program, not a port. Its goldens were made by running the program and checked by hand; the `fmt`, `fmt-indent-4`, `fmt-tab-stdin` and `min` goldens are byte-identical to `jq .`, `jq --indent 4 .`, `jq --tab .` and `jq -c .` (jq 1.7), except that jq rewrites the number `1E9` as `1E+9` and jsonv keeps a number's text. The `malformed` case fails at the line and column jq names (`3:30`).

## Layout

The program is a module of packages (`ergo.mod`: `module jsonv`), in the Go layout:

| folder | what it holds |
| --- | --- |
| `json/` | The codec. `value.ergo` (the union `Value`, `Member`, `Kind`, `Get`, `Equal`), `parse.ergo` (the scanner, `Parse`, its six typed failures, the union `SyntaxError`, `Describe`, `Offset`, `Locate`), `print.ergo` (`Compact`, `Indent`, `Quoted`), `laws.ergo`, `tests.ergo`. Pure. |
| `schema/` | The schema language. `schema.ergo` (the union `Schema`, `Equal`, `Infer`, `Join`), `validate.ergo` (`Validate`, `Check`, the union `Violation`, `Describe`, paths), `codec.ergo` (`FromJSON`, `ToJSON`, `BadSchema`), `laws.ergo`, `tests.ergo`. Pure. |
| `app/` | The commands. `documents.ergo` (the contract `Documents`, an operation type with the law `ReadIsRepeatable`, and its failures), `commands.ergo` (the union `Cmd`, the flags, `Parse`), `run.ergo` (`Load`, `Answer`, `Run`). |
| `memory/` | `Shelf`, the in-memory `Documents` for tests. |
| `files/` | `Disk`, the host's `Documents`: a path is read through the `fs.Files` value it holds, `-` through its `io.Stdin` value, kept after the first read. `tests.ergo` drives it over an in-memory file system and a standard input that drains. |
| `cmd/jsonv/` | The binary. `main.ergo` binds the host's `os.Process`, `io.Stdout` and a `files.Disk`, and is the only place that chooses them. Its tests run `app.Run` over a `memory.Shelf`. |

## What it shows

- **A union for the data, exhaustive `match` for every rule.** `Value = Null | Bool | Number | Text | Array | Object`; `Equal` matches the pair `(a, b)`, so Maranget's check sees every pair of kinds. `Schema` and `Violation` are unions too, and `Describe` and `PathOf` name every variant, with no `case _`.
- **Typed failures with positions and paths.** `Parse` fails `Unexpected | BadEscape | BadNumber | ControlInString | InvalidUTF8 | TooDeep`, each with the byte offset; `Locate` makes a line and a column. A schema violation carries a path from `$` (`$.home.zip`, `$["a b"]`), and a bad schema names the path in the schema (`$.properties.when.type`). `app.Run` catches each failure by name and turns it into an exit status.
- **A pointer-receiver scanner.** The parser is a Go programmer's recursive descent over `*scanner`, with a depth limit (`MaxDepth`), and runs of plain bytes taken as one slice.
- **Laws.** `json` states five laws (`CompactRoundTrips`, `IndentRoundTrips`, `IndentIsIdempotent`, `CompactIgnoresLayout`, `FailuresPointIntoTheText`), `schema` six (`AnythingAcceptsAll`, `InferredAccepts`, `JoinAcceptsBoth`, `SchemaRoundTrips`, `OtherKindsAreRejected`, `PathsAreRooted`), and `app.Documents` one, `ReadIsRepeatable`, which `files.Disk` keeps by remembering standard input. Each package's tests restate them as property tests over generated `Value`s, and `cmd/jsonv` checks that `check` accepts what `infer` prints.
- **A contract in the domain, providers named after their dependency.** `app` declares `Documents`; `memory.Shelf` and `files.Disk` provide it; `main` chooses `files.Disk`.

## Run it

```sh
go build -o bin/ergo ./cmd/ergo
./bin/ergo run examples/jsonv/cmd/jsonv fmt examples/jsonv/testdata/kitchen.json
./bin/ergo test examples/jsonv/json
go run ./scripts/examples jsonv
```

## Cases

`fmt`, `fmt-indent-4`, `fmt-tab-stdin`, `min` (the layouts), `check-ok`, `check-violations`, `check-stdin` (every kind of violation, from a file and from standard input), `bad-schema`, `malformed`, `missing`, `infer`, `usage`, `bad-indent` and `help`.

## Limits found

Each is written up with a repro in the lane's gap file (`gaps-ex-d.md`).

- **G-ex-d-1** No rune or byte converts to its text. A `\u` escape is decoded by spelling `\UXXXXXXXX` and calling `strconv.Unquote` (`runeText`); one byte of the source is `strings.Slice`.
- **G-ex-d-2** A union of error types cannot name a row, so `Parse`'s six failures are written out in five signatures beside the union `SyntaxError`.
- **G-ex-d-3** `strconv.FormatInt`, `utf8.ValidString`, `strings.ToValidUTF8` and `math.Trunc` are not in std; the program binds them with user externs, which the pure allowlist admits.
- **G-ex-d-4** A property test's strings come from a fixed alphabet with no quote, backslash or control byte; the tests map them through `spice` to reach the escapes.
- **G-ex-d-5** A type that holds a slice has no `==`: `json.Equal` and `schema.Equal` are written by hand.
- **G-ex-d-6** No std json and no derived codec (#49): `schema/codec.ergo` is the hand codec a derivation would write.
- **G-ex-d-7** `ergo fmt --check` takes files, not a package folder.
- **G-ex-d-8** `jsonv min` of 8 MB is 1.8x jq's time and 2.3x its memory (one run, loaded host); the Go control is pending.
- Known before this program: `files.Disk` remembers standard input in a `Map` field, the one shared mutable cell (G-ex-b-3); a required positional argument's `Get` is an `Option` (std-strengths F1, S5a).
