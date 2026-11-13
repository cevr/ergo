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

The read-modify-write of the sibling Map (`sib = m.Value; sib[id] = …;
p.siblings[parent] = sib`) moves the entry out and writes it in place (the
entry move-out): nothing between the read and the store reads `p`, and
`sib` dies at the store. Under the entry rule alone it copied the sibling
Map's table, which costs the parent's fan-out; with every node under the
root that is the tree, and the pass is quadratic again (the growth guard
`TestFanOutGrowsLinearly` holds that case linear).

Counted receipt: allocations of one `Place` on a pass of n nodes: base
e02194103, the entry rule (MAPCOPY round 1, 983a6a13a), and the move-out
(scratch harness, Go's `runtime.MemStats`).

| pass | base | entry rule | move-out |
| --- | --- | --- | --- |
| 1000 nodes | 1666 allocations, 191348 B | 5.8 allocations, 372 B | 4.2 allocations, 228 B |
| 2000 nodes | 3169 allocations, 378261 B | 4.8 allocations, 324 B | 3.2 allocations, 180 B |

Copy sites in `Place`'s Go: 5, 3, 2. What remains is the path: it is
read from the parent's entry and stored under the child's key, so it is
copied at the read (the parent keeps its entry), and copied again at the
store because `len(path)` reads it after; the second copy is a known gap
(a read after the store needs no copy).
