# flattree

JSON (RFC 8259) read into a flat tree, in the data-oriented form of Zig's and Yuku's ASTs, on the dense stores: every value is a 4-byte id into hand columns, children are runs of two pools, keys are interned. The same program reads the same text into a plain tree of unions (`-tree`), which is the reference the flat tree is held to.

```sh
flattree stats FILE            # values of each kind, depth, distinct keys
flattree path FILE POINTER     # the value at POINTER (RFC 6901), in jq's layout
flattree print FILE            # FILE on one line
flattree gen -seed S -n N      # write the workload: an array of N records for seed S
```

`stats`, `path` and `print` take `-tree` after the command word, and print the same bytes.

```text
$ flattree stats sample.json
values 29
depth 6
null 1
bool 1
number 13
string 4
array 7
object 3
keys 12
$ flattree path sample.json /twice
2
$ flattree print malformed.json
flattree: malformed.json:3:19: expected a value, found ","
```

## The flat form

`flat.Tree` holds no pointer per value. Its parts:

- **Ids and hand columns.** A value is a `dense.ID[Node]`. `Node` is a tag whose table holds no bytes; it hands out the ids. The value itself is three `dense.Col[Node, _]` columns: `kind` (a byte), `a` and `b` (`uint32`), 9 bytes a value. What `a` and `b` mean depends on the kind: a number's span of the source, a string's span or interned text, a container's run.
- **Runs in pools.** An array's items are a `dense.Run` in one `dense.Pool[dense.ID[Node]]`. An object's members are a run in a `dense.Pool[Member]`, where `Member { Key dense.ID[Key], Value dense.ID[Node] }`. While a container is open, it gathers its children's ids in a slice of its own. When it closes, they move into the pool in one `PutList`. (A scratch stack shared by all containers, as the control has, would need `slices.Slice` of a field that is written later, and today that clones the whole stack at every close.)
- **Keys interned.** Every key, and every string with an escape or a byte outside ASCII, is interned in `flat.Keys`: one id per distinct text. Two members name one key exactly when their key ids are equal, so `flat.Get` looks a pointer's token up once and then compares ids, not text. A string with no escape and only ASCII is a span of the source, not a copy.
- **Children before parents.** A value is put when it closes, so a child's id is less than its parent's. The tree has no cycle by construction, the root is the last id, and `flat.StatsOf` is one loop over the ids in order with no recursion: a child's depth is known before its parent's is taken.
- **Diagnostics as rows.** A problem is a row of a `dense.Table[tree.Malformed]`, and each rule stops at the first one. `flat.Parse` fails once, at its boundary, with the first row. It is the failure `tree.Parse` gives at the same byte.

A reader never sees the columns. `flat.ViewOf(t, id)` is a `View`, a small union (`Null`, `Bool`, `Number { At, Len }`, `Str { S }`, `Array { Items }`, `Object { Members }`), so every read is an exhaustive `match`. `flat.Encode` is its inverse.

## What it shows

- **A dense form with its plain form beside it** (NORTH_STAR, laws). `flat/laws.ergo` holds the flat tree to package `tree`:
  - `FlatEqualsTree`: both printed forms of every generated value read flat as `tree` reads them, with the same stats.
  - `FailsWhereTheTreeFails`: a text with a byte changed fails flat where it fails in the tree, with the same failure.
  - `PrintRoundTrips`.
  - `PointersReachTheTreesPlaces`.

  The encoding has its own laws too:
  - `ViewRoundTrips`: the view of every encoded variant is the variant.
  - `ChildrenStayInside`: every child id is less than its parent's, and the runs cover their pools exactly.
  - `KeysAreInterned`: key ids are equal exactly when the texts are.
- **Unions and exhaustive `match`** over the view, and over `tree.Value` in the plain twin.
- **A machine in the parser.** Both sides read a number with `tree.Numeral`, the machine of PA1's jsonparse.
- **Typed failures** with positions: `tree.Malformed`, `tree.BadPointer | tree.NoValue`. Also `app.Documents.Repeatable`, the law of the document contract, run on both providers.

## Layout

- `flat/`: the flat tree (`flat.ergo`), its parser, printers, pointer walk and laws.
- `tree/`: the plain twin. It is examples/jsonparse's package `json` under a new name, plus `stats.ergo`: `Stats`, `StatsOf` and `Equal`.
- `dense/`: a byte copy of std `dense` at wf10/dn1 b01688b9a (DOD DN1). It stays here until the compiler lines merge; then the import becomes std `dense` and the copy goes.
- `app/`: the `Documents` contract and the commands, each on a `Side` (`Flat` or `Plain`).
- `files/`, `memory/`: the `Documents` providers.
- `workload/`: the generator of the receipts' document.
- `cmd/flattree/main.ergo`: binds `files` over `fs.NewHost()`.
- `testdata/JSONTestSuite-1ef36fa0/`: [JSONTestSuite](https://github.com/nst/JSONTestSuite)'s `test_parsing` files at that commit, unchanged, with its MIT license.

## The Go control

`cmd/ergo/testdata/flattree/control` is the same flat form in hand-written Go, as a Go programmer writes it:
- the columns are preallocated from the text's length;
- a container's run is held in its own `a` and `b`;
- keys go in a `map[string]uint32`;
- the stats are one pass with a `switch` on the kind.

`TestFlatTreeAgreesWithControl` (cmd/ergo, full tier) runs three sides: the Ergo program flat, the same program `-tree`, and the control. It runs them on every JSONTestSuite file, on seeded generated documents and on each with bytes changed, and on the workload. It checks that all three print the same bytes, and what a reference on encoding/json computes. The receipts (instructions, allocations and bytes per input byte, bytes per value) are files of the run, not tests.
