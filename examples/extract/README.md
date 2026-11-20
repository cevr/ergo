# extract

Reads the receipt in a document by asking a language model for it, as a typed function: `extract RECORDING DOCUMENT` asks for a `receipt.Receipt` of the schema `ergo gen schema` wrote for it, reads the reply, and prints the receipt, each repair its reply needed, and each problem the receipt has (a total that is not its items' sum). A reply that is no receipt exits 1 with the reply and the place it breaks; nothing is ever filled in with a default.

Tag: `default`. It is an original program, not a port: BAML's receipt extraction (its README's `ExtractReceipt` function) on neutral data. The model is a recording (`llm/recorded`): the cases replay it with no network and no key. Each recording under `testdata/` was made by `cmd/record` from a reply written by hand in a shape models give (a bare object, a fence in prose, comments, bare keys, single quotes, trailing commas, a string where a number goes, a reply cut at its length) and checked by hand; the goldens were made by running the program.

## Layout

The program is a module of packages (`ergo.mod`: `module extract`), in the Go layout:

| folder | what it holds |
| --- | --- |
| `receipt/` | The domain. `receipt.ergo`: `Receipt`, `Item`, the union `Payment` (`Card` or `Cash`), the union `Problem`, `Sum`, `Check` with its law `CheckFindsTheTotal`, and how each prints. Pure. |
| `receipt/receiptschema/` | `Receipt()`, the JSON schema of `Receipt`'s wire form, and its law `ReceiptValuesFitTheSchema`: written by `ergo gen schema examples/extract/receipt.Receipt`, never by hand. |
| `reader/` | `Read(document) Outcome needs llm.Model`: one `llm.Ask` for a `Receipt` of that schema. `Outcome` is a closed union, `Found` (the receipt and its `json.Fix`es), `Unfit` (the `llm.NoFit`) or `Failed` (the model's `llm.Failure`), so the command matches every one. Its law `ReplyReadsBack` runs over `llm/memory`'s `Sequence`: the codec's JSON of any receipt, bare or fenced, reads back as the receipt, and the same reply stopped at its length is `Unfit`. |
| `cmd/extract/` | The binary. It binds the host's file system and a `recorded.Replay` of the recording, and is the only place that chooses the model. |
| `cmd/record/` | Makes a recording: `record DOCUMENT REPLY [length]` asks as `extract` does, through `recorded.Record` over a model that says REPLY, and prints the recording's lines. Bound to a real provider instead, it records a real model once. |

## What it shows

- **A typed ask.** `llm.Ask[T]` is `Format` (the schema in the request), one `Generate`, and `Parse` (std `json`'s loose reader): the program declares the type and gets the type, with the repairs named (`repaired: cut from a fence`), or a `NoFit` that holds the reply.
- **One form for the schema and the codec.** The schema is generated from the same declaration the codec is derived from, and its generated law checks that every receipt's JSON fits it, so the schema a model is asked for cannot drift from what the program reads.
- **Exhaustive `match`.** The outcome, the payment, the problem and the fix are closed unions; a new variant does not build until each `match` names it.
- **Laws beside their declarations.** `CheckFindsTheTotal` lives with `Check`, `ReplyReadsBack` with `Read`.
- **A cut reply is not a value.** The `cut` case is a reply that stopped at its length: the loose reader could close it, but `Ask` never parses it.
