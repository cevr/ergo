# tomlparse

A TOML 1.0 parser written in Ergo. It reads TOML text into a tree of values and prints the tree again, as TOML or as [toml-test](https://github.com/toml-lang/toml-test)'s tagged JSON. Three things fail with their line and column: a text that is not TOML, a key defined twice, and a key used as what it is not.

```sh
tomlparse json FILE            # FILE as toml-test's tagged JSON, on one line
tomlparse fmt FILE             # FILE printed again as TOML
tomlparse check FILE           # how many values FILE holds and how deep they nest
tomlparse gen -seed S -n N     # write the workload: N [[record]] tables for seed S
```

```text
$ tomlparse json twice.toml
tomlparse: twice.toml:7:2: key server is defined twice
$ tomlparse json clash.toml
tomlparse: clash.toml:4:8: key fruit.apple is a table dotted keys defined, which a header cannot define
```

## What it reads, and how

- **All of TOML 1.0.** That includes:
  - every string form: basic with every escape (`\uXXXX` and `\UXXXXXXXX` of scalar values only), literal, multi-line basic with line-ending backslashes, and multi-line literal;
  - integers in decimal, hex, octal and binary, with underscores between digits only;
  - floats with `inf` and `nan`;
  - booleans, and offset date-times, local date-times, local dates and local times, each checked against the calendar;
  - arrays over lines with comments, inline tables, dotted and quoted keys, `[tables]` and `[[arrays of tables]]`;
  - a byte-order mark;
  - LF or CRLF line ends.

  The text must be UTF-8. Control characters other than tab may not stand in comments or strings.
- **What a key may become is a machine.** `toml.Definition` reads each use of a key and moves it through these states:
  - `Absent`;
  - `Implicit`: a header named a table below it;
  - `Header`;
  - `Dotted`;
  - `Tables`: an array of tables;
  - `Closed`: a value, an inline table included. Nothing can be added to it, so nothing can be added to what is in it either.

  The uses are `Pass`, `Open`, `Append`, `Dot` and `Set`. Its step function is TOML's rules for tables, and a use those rules forbid ends the machine with the failure it is:
  - `Duplicate{Key}` for a key defined twice;
  - `Clash{Key, Why}` for a key used as what it is not: a table a header defined that dotted keys extend, a header over a table that dotted keys defined, an array of tables used as a table, or anything added to a value.
- **A node store, then a tree.** While it reads, the parser keeps every key, array element and table of an array of tables as a node in one slice. Each node holds:
  - its parent;
  - its name;
  - its definition state;
  - what it holds.

  A `Map` from (parent, name) finds a key. At the end one pass groups the nodes by parent and builds the `toml.Value` tree: a union of seven variants.
- **Output.**
  - `json` writes a table's keys in byte order and a scalar as `{"type": T, "value": V}`:
    - a date or time has its separator `T` and its zone `Z` upper case;
    - a float is `inf`, `-inf`, `nan` or Go's shortest form.
  - `fmt` writes these in the text's order:
    - first a table's plain members, then its tables as `[a.b]` and its arrays of tables as `[[a.b]]`;
    - strings as basic strings, integers in decimal.
- **Failures** are `tomlparse: FILE:LINE:COL: reason`, status 1. The column counts bytes. A usage failure is status 2.

## What it shows

- **A machine with laws.** `toml.Definition`'s laws are of two kinds:
  - proved on its graph: `HeaderAfterImplicit`, `DottedAfterImplicit`;
  - tested over every use: `ClosedTakesNothing`, `SetOnlyOnce`.
- **Unions and exhaustive `match`.** These are all unions, and the checker holds every `match` over them total:
  - `Value` (seven variants), `Clock`, and the machine's `Shape`, `Use` and `Refusal`;
  - the builder's `Form`.
- **Typed failures.** `toml.Parse` fails with `Malformed | Duplicate | Clash`, and `app.Load` turns each into the line the user reads.
- **Laws against models.**
  - `toml.PrintParsesBack` prints generated trees as TOML and reads them back.
  - `toml.RedefinedKeysFail` puts a key of a generated tree before its text once more. It must fail as a `Duplicate` where the text gives the key a value, and as a `Clash` where a header makes it a table.
  - `workload.DocumentIsTOML` holds the workload to TOML.
  - `app.Documents.Repeatable` is the document contract's law.

## Layout

- `toml/`: values, the definition machine, the parser (`parse.ergo`, `text.ergo` for strings, `scalar.ergo` for numbers, dates and times), the printers, the generator and the laws.
- `app/`: the `Documents` contract and the commands, from a document's name to output, error line and status.
- `files/`: `Documents` over a file system (`fs.Files`). `NewDisk` builds one over an `fstest.Memory`, so `Repeatable` runs on it.
- `memory/`: `Documents` over texts in memory.
- `workload/`: the generator of the receipts' document.
- `cmd/tomlparse/main.ergo`: binds `files` over `fs.NewHost()`.
- `testdata/toml-test-96fd0884/`: toml-test's TOML 1.0 files at that commit, unchanged, with its MIT license (`SOURCE` names the pin).

## The Go control

`cmd/ergo/testdata/tomlparse/control` is the same design in hand-written Go:
- the same node store and definition rules;
- the same tree;
- the same printers and failure lines.

`TestTOMLParseAgreesWithControl` (cmd/ergo, full tier) runs both binaries on:
- every toml-test file: a valid file must print the suite's JSON, compared as toml-test compares it, and read back the same after `fmt`; an invalid file must fail;
- seeded documents written from a generated tree in random forms: each must print the tree's JSON;
- the same documents with a key defined twice, which must fail;
- the same documents with bytes changed;
- the workload.

Both binaries must print the same on every run.

The receipts are files of the run, not tests. They measure instructions, allocations, GC share and peak memory per byte, against the control.
