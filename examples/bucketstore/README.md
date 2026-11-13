# bucketstore

Named buckets of counters in one `sync.Ref`. `store.Bump` changes one
counter of one bucket inside the Ref's owned callback (`sync.Modify`): the
callback owns the Map it is given, so it reads the bucket with `Get` and
writes it back with one entry write.

`bucketstore N` bumps N counters spread over 64 buckets and prints the
bucket count, two counts and the sum of the counts `Bump` returned.

## Layout

- `store/`: `Store`, `Bucket`, `Bump`, `Count`, `Buckets`, and the laws
  `BumpCounts` (a counter bumped n times counts n) and `BucketsApart` (a
  bump of one bucket leaves another unchanged).
- `cmd/bucketstore/`: the binary.

## What it shows: the entry rule

A Map entry has no write path in Ergo: `m[k].f = v` is refused, and a
write replaces a whole entry. So a read of an entry (`m.Get(b)`) keeps
nothing that a later write through `m` can change, and the lowering copies
nothing at the read (docs/spec/types.md, Bindings). A copy of a Map is a
copy of its table (`maps.Clone`), never of every entry.

Counted receipt: allocations of one `Bump` on a store of K buckets, from
the Go of the program built by the compiler before the rule (base
e02194103) and at the rule's head (scratch harness, Go's `runtime.MemStats`).

| store | before | after |
| --- | --- | --- |
| 500 buckets | 3015 allocations, 426377 B | 9 allocations, 1072 B |
| 1000 buckets | 6017 allocations, 851376 B | 9 allocations, 1072 B |

Copy sites in `Bump`'s Go: 3 before (the whole store, entry by entry, at
the `Get`; the bucket, deep; the cell's copy, entry by entry), 2 after: the bucket the callback
changes (a shallow copy of its two Maps: the entry may be shared with an
earlier read, which the compiler cannot rule out) and the cell's
copy-if-shared function, which runs only when the cell's value is shared.
