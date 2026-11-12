# jsonparse

A JSON parser written in Ergo: RFC 8259 text to a tree of values, and the tree back to text. A text that is not JSON fails at its first byte that is not, with the line and column.

```sh
jsonparse fmt FILE            # FILE in jq's layout
jsonparse min FILE            # FILE on one line
jsonparse get POINTER FILE    # the value at POINTER (RFC 6901), in jq's layout
jsonparse check FILE          # how many values FILE holds and how deep they nest
jsonparse gen -seed S -n N    # write the workload: an array of N records for seed S
```

```text
$ jsonparse get /nested/a~1b sample.json
true
$ jsonparse fmt malformed.json
jsonparse: malformed.json:3:19: expected a value, found ","
```

## What it reads, and how

- **The grammar is RFC 8259's**, with encoding/json's choices where the RFC leaves one: whitespace is space, tab, newline and return; one value with whitespace around it; arrays and objects nest at most 10000 deep (`json.MaxDepth`), and one more fails at its bracket instead of overflowing the stack.
- **A number keeps its text.** `1.0e0` prints as `1.0e0` and `123456789012345678901234567890` loses no digit; nothing converts it to a float.
- **A string is its decoded bytes**, decoded as encoding/json decodes them: a byte that is not UTF-8 reads as U+FFFD, and so does a `\u` escape of a surrogate that is not half of a pair. A string with no escape and only ASCII is a slice of the text, not a copy.
- **A key may repeat.** An object keeps its members in order, both of a repeated key's; a pointer names the last, as a reader that keeps one value per key does.
- **Printing** escapes `"`, `\`, the control bytes and DEL (`\n`, `\t`, `\r`, `\b`, `\f`, else `\u00XX`) and writes every other byte as it is. `fmt` is jq's layout: two spaces per level, `[]` and `{}` for empty containers.
- **Failures** are `jsonparse: FILE:LINE:COL: reason`, status 1; the column counts bytes. A usage failure is status 2.

## What it shows

- **A machine in the parser.** A number is read by the machine `json.Numeral` (start, minus, zero, whole, point, fraction, mark, sign, power); each byte is a signal of its class, and the grammar is its step function. Its laws are proved on its graph (`ExponentTakesASign`, `FractionFollowsZero`: a run reaches the path) or tested (`ZeroEndsItsInteger`).
- **Unions and exhaustive `match`.** `json.Value` is a union of six variants; `Count`, `Depth`, the printers and the pointer walk are matches the checker holds total.
- **Typed failures with positions.** `json.Parse` fails with `json.Malformed{Offset, Line, Col, Reason}`; `json.Get` fails with `BadPointer | NoValue`; `app.Load` turns them into the line the user reads.
- **Laws against models.** `json.PrintParsesBack` prints generated values both ways and reads them back; `json.PointersReachTheirPlaces` gets every place of a generated value by its pointer. `app.Documents.Repeatable` is the document contract's law, run on both providers.

## Layout

- `json/`: values, the number machine, the parser, the printers, pointers, the generator and the laws.
- `app/`: the `Documents` contract and the commands, from a document's name to output, error line and status.
- `files/`: `Documents` over a file system (`fs.Files`); `NewDisk` builds one over an `fstest.Memory`, so `Repeatable` runs on it. `memory/`: `Documents` over texts in memory.
- `workload/`: the generator of the receipts' document.
- `cmd/jsonparse/main.ergo`: binds `files` over `fs.NewHost()`.
- `testdata/JSONTestSuite-1ef36fa0/`: [JSONTestSuite](https://github.com/nst/JSONTestSuite)'s `test_parsing` files at that commit, unchanged, with its MIT license (`SOURCE` names the pin).

## The Go control

`cmd/ergo/testdata/jsonparse/control` is the same design in hand-written Go: the same recursive descent over the whole text, one tagged value struct, the same printers. `control check -std FILE` counts the same document through encoding/json, for scale. `TestJSONParseAgreesWithControl` (cmd/ergo, full tier) runs both on every JSONTestSuite file (`y_` accepted, `n_` rejected), on seeded generated documents and on each with a few bytes changed, and on the workload, and checks that both print what a reference on encoding/json computes: its tree printed by the test's own printers, and the position of the first byte encoding/json cannot read on from. The receipts (instructions, allocations, GC share and peak memory per byte, against the control) are files of the run, not tests.
