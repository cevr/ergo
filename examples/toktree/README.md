# toktree

The counted workload for the data layout of the self-hosted compiler (wf10 self-layout plan §2.3; receipt [docs/receipts/self-layout.md](../../docs/receipts/self-layout.md)). It reads a file of source text, scans it into token columns, builds the flat tree of its bracket groups, reads every token and every child list once, and prints counts and two sums.

```text
$ toktree sample.ergo
tokens=71 nodes=62 groups=8 depth=4 pool=61 toksum=381448556 nodesum=1242744704
```

It is the parser's data movement without the grammar, so the instructions per byte bound what the layout costs a parser on std `dense` against hand Go.

## The layout

- **Tokens** (`token.Toks`): two columns, `kind` (`uint8`) and `start` (`uint32`), keyed by `dense.ID[Tok]`. No end and no text are kept. A token's text is found by scanning again, as in Zig's tokenizer. The scanner is a byte-class scanner (identifiers, numbers, strings, runes, comments, brackets, punctuation), not Ergo's grammar.
- **Tree** (`tree.Tree`): one row per node in three columns: `kind`, the main token, and the run of its children. The child ids sit in one `dense.Pool`. Children are put before their parent, so the root is the last row and a forward loop is a post-order walk.
- **Scratch stack.** Lists are gathered on one scratch stack with marks. The stack keeps its top in an integer: `stack = stack[0:mark]` lowers to a copy today (OWN1), and on 4.5 MB of input that form allocated 19.78 GB.

## Laws

- `token.StartsAdvance`: the starts strictly increase, and none is on whitespace.
- `token.BracketsAreTheirOwnTokens`: a bracket token is one byte of its kind.
- `tree.ChildrenBeforeParent`.
- `tree.OneParentRootLast`: every row but the root is the child of exactly one row, and the pool holds exactly those children.
- `tree.GroupsAreOpens`: one group per open bracket, and no leaf is a comment or an open bracket.

The laws run over text generated from fragments (`token.Sample`) that reach every branch of the scanner.

## The receipt

```sh
go run ./scripts/opcount -control -rounds 5 toktree
```

The `workload` file sets the sizes and the input:
- the sizes are 4,000,000 and 32,000,000 bytes, and one unit is a byte of input;
- the input is the repository's own `.ergo` files under `internal/sketch/std` and `examples`, in path order, repeated and cut to size (`cmd/ergo/testdata/toktree/gen`). The input follows the tree, so a receipt names its commit.

The control, `cmd/ergo/testdata/toktree/control`, is the same layout in hand Go:
- by default its columns get a capacity from the input's size;
- `-nocap` gives no capacity, which is all std `dense` can say before SL2;
- `-sub` builds Carbon's form, which has no child pool.

Every later unit of the self-layout plan reports the Ergo / hand Go ratio of this receipt.

## Layout

- `token/`: the token columns, the scanner and its laws.
- `tree/`: the tree store, its builder and its laws.
- `cmd/toktree/main.ergo`: reads the file through `fs.Files` and prints the summary.
- `testdata/sample.ergo`: the cases' input.
