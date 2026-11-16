# pager

A scrollback buffer of lines and the pages a reader marks. A mark keeps a
window of the buffer (`slices.Slice`); the buffer is redrawn after, and
the mark still shows the lines as they were.

`pager N` draws N frames into a 4000-line buffer, each redrawing eight
lines and marking a 24-line page, and prints the page count and the first
line of the first and last page.

## Layout

- `screen/`: `Screen`, `Draw`, `Mark` (returning `Page | OutOfRange`),
  `PageOf`, `Line`, and the laws `MarkKeepsThePast` and `MarkOutOfRange`.
- `cmd/pager/`: the binary.

## What it shows: a window is copied, not its buffer

`slices.Slice(s.lines, from, to)` is a pure std body whose result shares
its argument. The page is kept and the buffer written after, so one of
them must be copied: the lowering copies the result, the window, instead
of the argument, the buffer (docs/spec/types.md, Bindings).

Counted receipt: allocations of one `Mark` on a buffer of n lines, base
e02194103 before, the rule's head after (scratch harness, Go's
`runtime.MemStats`).

| buffer | before | after |
| --- | --- | --- |
| 4000 lines | 2.1 allocations, 66010 B | 1 allocation, 462 B |
| 8000 lines | 2.0 allocations, 131535 B | 1 allocation, 462 B |

Copy sites in `Mark`'s Go: 2 before, 1 after: the window.
