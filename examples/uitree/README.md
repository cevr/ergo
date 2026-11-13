# uitree

One layout pass over a tree, as a UI pass places its nodes: each node
reads its parent's sibling index out of a Map of Maps and its parent's
path out of a Map of slices, through a borrow (`*Pass`), then writes its
own entries.

`uitree N` places nodes 1 to N, four children a parent, and prints the
deepest level, the path of node N and the root's children.

## Layout

- `layout/`: `Pass`, `Place` (returning the union `At | NoParent`, matched
  exhaustively), `Path`, `Children`, and the laws `SlotsInOrder`,
  `UnplacedParent` and `PathExtends`.
- `cmd/uitree/`: the binary.

## What it shows: reads through a borrow copy nothing

`p.paths.Get(parent)` reads one entry through the borrow. A Map entry has
no write path, so the read keeps nothing that the later `p.paths[id] = …`
can change, and the lowering copies nothing (docs/spec/types.md,
Bindings). Before the rule each `Get` copied the whole Map entry by entry,
so a pass of n nodes cost n² (the growth guard
`TestUIPassGrowsLinearly`, internal/sketch/lower, holds it linear).

Counted receipt: allocations of one `Place` on a pass of n nodes, base
e02194103 before, the rule's head after (scratch harness, Go's
`runtime.MemStats`).

| pass | before | after |
| --- | --- | --- |
| 1000 nodes | 1666 allocations, 191348 B | 5.8 allocations, 372 B |
| 2000 nodes | 3169 allocations, 378261 B | 4.8 allocations, 324 B |

Copy sites in `Place`'s Go: 5 before, 3 after. What remains is the
read-modify-write of an entry (`sib = m.Value; sib[id] = …; p.siblings[parent] = sib`):
the sibling Map is copied, its table only, since the entry may be shared
with an earlier read; that costs the parent's fan-out (here four), not the
tree.
