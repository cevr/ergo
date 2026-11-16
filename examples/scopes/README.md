# scopes

A symbol table of nested scopes, as a resolver walks a program: a Map of
scopes, each a Map of names, and a Map of parents, read and written
through a borrow (`*Table`).

`scopes N` opens N scopes in chains eight deep, declares one name in each,
resolves every name from the innermost scope of its chain and prints how
many resolved, how far out they were found, and their sum.

## Layout

- `table/`: `Table`, `OpenScope` (returning `Open | Taken | NoScope`),
  `Declare`, `Resolve` (returning `Bound | Unbound`), and the laws
  `InnerShadows`, `FoundOutward` and `OpenOnce`.
- `cmd/scopes/`: the binary.

## What it shows: a lookup through a pointer copies nothing

`Resolve` reads `t.scopes.Get(at)` and `t.parents.Get(at)` through the
borrow at every step. A Map entry has no write path, so no read keeps
anything a write through `t` could change, and the lowering copies
nothing (docs/spec/types.md, Bindings). Before the rule each `Get` through
the pointer copied the whole Map of scopes, entry by entry.

Counted receipt: allocations of one `Resolve` that walks eight scopes, on
a table of n scopes, base e02194103 before, the rule's head after (scratch
harness, Go's `runtime.MemStats`).

| table | before | after |
| --- | --- | --- |
| 1000 scopes | 16056 allocations, 2344353 B | 0 allocations, 0 B |
| 2000 scopes | 32088 allocations, 4687872 B | 0 allocations, 0 B |

Copy sites in `Resolve`'s Go: 1 before (the Map of scopes, at each step),
0 after.
