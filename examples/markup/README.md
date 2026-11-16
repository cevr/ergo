# markup

Renders a document (a slice of `Heading | Para | Quote | Rule` blocks,
matched exhaustively) as lines of markup. Each block adds its lines in one
chain of Appends: `out = out.Append("<p>").Append(p.Text).Append("</p>")`.

`markup N` renders N sections and prints the line count and the first and
last line.

## Layout

- `render/`: `Block`, `Lines`, `Tagged`, and the laws `ThreeLinesABlock`,
  `QuoteIndents` and `TaggedKeepsItsInput` (Tagged leaves its input as it
  was).
- `cmd/markup/`: the binary.

## What it shows: a chain of Appends writes in place

In `x.Append(a).Append(b)` the second Append's source is the slice the
first one made, which nothing else holds, so it writes that slice in
place. On a `let mut` root (`out = out.Append(…).Append(…)`) the first
link writes in place too (docs/spec/types.md, Bindings). A chain on a
shared source, as `Tagged`'s `lines`, copies once, at its first link.
Before the rule every link copied the slice, so a render of n blocks
cost n².

Counted receipt: allocations of `Lines` per block of a document of n
paragraphs, base e02194103 before, the rule's head after (scratch harness,
Go's `runtime.MemStats`).

| document | before | after |
| --- | --- | --- |
| 1000 blocks | 3 allocations, 106722 B a block | 0.0 allocations, 158 B a block |
| 2000 blocks | 3 allocations, 198916 B a block | 0.0 allocations, 173 B a block |

Copy sites in `Lines`'s Go: 8 before, 0 after.
