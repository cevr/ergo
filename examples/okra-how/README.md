# okra-how

A port of `okra how` from the owner's okra CLI (`origin`): it prints the table of contents of okra's guides, or the whole guide of one topic. Its goldens come from the original: `go run ./scripts/examples -record okra-how` runs `record.sh`, which runs the `okra` on `PATH`.

Tag: `default`.

## Layout

The program is a module of packages (`ergo.mod`: `module okra`), in the Go layout:

| folder | what it holds |
| --- | --- |
| `how/` | The domain. `topic.ergo` (`Topic` and `Parse`, which reads a guide's front matter line by line through `io.Input`'s `Lines`), `catalogue.ergo` (`Catalogue`, `Load`, `Of`, `Find`, `Subtopics`), `answer.ergo` (`Request` and the text the command prints), `library.ergo` (`Library`, the contract it needs, with its laws), `laws.ergo` and `tests.ergo`. No function needs a capability except `Load`, which needs `Library`. |
| `memory/` | `Shelf`, the in-memory provider of `how.Library`, and `Disk`, an in-memory provider of `fs.Files` for tests. |
| `files/` | `Read`, which reads a shelf from a directory through `fs.Files`. |
| `cmd/okra-how/` | The binary. `main.ergo` reads the command line, binds the host's `fs.Files` and `os.Process`, and is the only place that chooses a provider. |
| `testdata/guides/` | okra's guides, `NAME/SKILL.md`, byte for byte at the commit `origin` names, and `topics`, the order of the contents. |

## What it shows

- **A contract with laws.** `how.Library` is an operation type with two member laws: `NamedOnce` (the contents name each topic once) and `GuideIsRepeatable` (two reads of a guide agree). `memory/tests.ergo` runs their checks against a `Shelf`.
- **A sorted index by construction.** `Catalogue` holds its names as a `slices.Ascending[string]`, which only `slices.Ascend` builds, and `Find` is `slices.BinarySearch` over it: an unsorted index is a check error, not a wrong answer. `Of` refuses two topics of one name with `Duplicate`, so a binary search never meets two equal names.
- **Typed failures, matched exhaustively.** `answer` fails `how.NotFound | how.Unreadable | how.Duplicate`; `parse` fails `NoCommand | UnknownCommand | ExtraArgument`. `run` catches each one by name, with no `case _`, and ends the program with `os.Exit`: a usage error with status 2, a domain failure with status 1 and okra's `[CODE] message` form. A new failure stops the build until `run` handles it.
- **Unions and `match`.** `how.Request = Contents{} | Guide{Name string}` is what the command asks; `Answer` matches it exhaustively.
- **Laws as properties.** Top-level laws in `how/laws.ergo` state the rules: `FindsEveryTopic`, `MissNamesTheContents`, `FrontMatterIsRemoved` and `FirstSentenceIsAPrefix`. `Parse` joins the lines after the front matter back into the body, which `io`'s law `LinesSplitInput` allows.
- **Providers chosen once.** A provider of `how.Library` cannot need a capability that the contract's operations do not write (rows.md, Context 2), so `files.Read` reads every guide at the start into a `memory.Shelf`, under `fs.Files`. `main` binds the host's file system; the tests bind a `memory.Disk` and an `io.Recorder`, with no host.

## Run it

```sh
go build -o bin/ergo ./cmd/ergo
cp -R examples/okra-how/testdata/guides /tmp/guides
OKRA_GUIDES=/tmp/guides ./bin/ergo run examples/okra-how/cmd/okra-how how repo
./bin/ergo test examples/okra-how/how
go run ./scripts/examples okra-how
```

The guides are read from `$OKRA_GUIDES`, or from `guides` in the working directory. Each case's `setup.sh` copies `testdata/guides` there.

## Declared differences

- okra embeds its guides in its binary; the port reads them from a directory when it starts. A missing or unreadable guide is `[UNREADABLE]`, and a topic listed twice is `[DUPLICATE]`, both with status 1; okra cannot meet either.
- okra's command line is @effect/cli's, with its help, `--version`, `--wizard` and its error layout. The port reads `how [<topic>]` only. A usage error prints a short message and `usage: okra how [<topic>]` to standard error with status 2, where okra prints its help with status 1, so no case covers it. `okra how -h` is a request for the topic `-h` in the port.
- The width of the table counts bytes, as JavaScript's `padEnd` counts UTF-16 units: the same for okra's ASCII names. Trimming is Go's `strings.TrimSpace`, which does not remove U+FEFF as JavaScript's `trim` does.

## What waits

- **std flag (batch-11 gaps G1).** No program of more than one file can call `flag.ParseCommand` yet: the build stops with an internal compiler error. So `parse` reads the arguments by hand. When G1 is fixed, `parse` becomes `flag.ParseCommand` over one `flag.Command[Cmd]` named `how` with `flag.OptionalArg("topic", …)`, and `run` ends its failures with `flag.Exit`. When SS4 lands, that command's `Build` may also fail with the program's own error.
- Laws run as generated tests when #162 lands; then the property tests that restate them go.

## Cases

`contents` (`okra how`), `repo` and `keys` (one guide each) and `unknown` (a topic okra does not have: status 1 and `[NOT_FOUND]` on standard error). Recorded from okra v0.13.0.
