# cborpack

A package registry's index written as CBOR (RFC 8949) by the module's own codec, and as JSON by std `json`. `cborpack hex N` prints the CBOR of an index of N packages in hex; `cborpack receipt N` prints the size of both encodings and the heap allocations of one encode done again, counted by Go's `testing.AllocsPerRun`.

Tag: `default`. It is an original program, not a port. The codec's goldens are the examples of RFC 8949 Appendix A (`cbor/tests.ergo`); the `hex` golden was checked by hand against the RFC's rules.

## Layout

The program is a module of packages (`ergo.mod`: `module cborpack`), in the Go layout:

| folder | what it holds |
| --- | --- |
| `cbor/` | The codec. `cbor.ergo`: `Item`, the writer (an operation type with the prelude's `Writer` events and the law `RawJoins`), its one provider `item`, and `Encode`. `tests.ergo`: the RFC's examples. |
| `registry/` | The domain: `Package`, `Release`, `Person`, the union `State`, `Index`, and `Sample`, an index of N packages. |
| `cmd/cborpack/` | The binary. `main.ergo` parses the command (a union, matched exhaustively), encodes, and counts. |

## What it shows

- **A module's codec takes std json's path.** Nothing in the compiler names `json`. `cbor.Item` is a sealed operation type with one provider, as std's `json.Text` is, so `encode(v, t)` on a `let t Item = item{…}` is a cell writer (types.md, Laws 14): each type's codec function takes and returns the `item` value, and each event is a direct call of `item`'s function, not a `Writer` interface call. The build unfolds each event by its definition (a constant key's head is computed at build time from the key's length) and joins the constant bytes by `RawJoins`, which `ergo test` proves and writes to `ergo.laws`: a record's map header and its first key are one constant append, `raw("\xbfdname")`, as std json's are `raw("{\"name\":")`.
- **The same buffer.** `Encode` borrows its buffer from std `strings` (`Hint`, `Borrow`, `Give`, `FromBytes`), as std json's `Encode` does, so `receipt` counts the same allocations for both: the result, and the order of each `Map`'s keys, which every codec's `encode` takes from the runtime. The hint is `item` writing that `T`'s own: a text of 58 KB borrows a 64 KiB buffer, the next encode reuses it, and an encode after a GC allocates that one buffer, not a 1 KiB one regrown (stdlib.md, strings rule 11).
- **Laws and matching.** `RawJoins` is the law the build rewrites by; `State` and the command are unions every `match` names in full.

## Run it

```sh
go build -o bin/ergo ./cmd/ergo
./bin/ergo run examples/cborpack/cmd/cborpack -- receipt 200
./bin/ergo test examples/cborpack/cbor
go run ./scripts/examples cborpack
```

## Cases

`hex` (an index of one package), `receipt` (two packages, under one pooled buffer), `large` (200 packages: the texts outgrow the least buffer, and each encode again reuses the one its hint chose) and `usage`.

## Limits found

- A `Map`'s key order costs about four allocations per `Map` (`v1.WireKeys` collects and sorts its keys), for every codec alike.
- A key of 256 bytes or more keeps its head's run-time writes (`wideHead`); a shorter key's head is constant.
- No decoder: `cbor` writes only.
- The receipt cannot count an encode after a GC: `testing.AllocsPerRun` takes a Go `func()`, whose row is empty, so its body cannot call `runtime.GC` (`go.Foreign`), and `runtime.ReadMemStats` needs a `MemStats` value Ergo cannot make (no zero value). The cold count is `runtime/v1`'s `TestBorrowAfterGCTakesItsHint`, for any writer.
