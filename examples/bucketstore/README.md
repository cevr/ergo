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

## What it shows: the entry rule and the entry move-out

A Map entry has no write path in Ergo: `m[k].f = v` is refused, and a
write replaces a whole entry. So a read of an entry (`m.Get(b)`) keeps
nothing that a later write through `m` can change, and the lowering copies
nothing at the read (docs/spec/types.md, Bindings).

`Bump` is a read-modify-write of one bucket: `k = v.Value`, a write
through `k`, `m[b] = k`. Nothing between the read and the store reads `m`
and `k` dies at the store, so the lowering moves the bucket out of the Map
and writes it in place (the entry move-out). To keep that sound, the
store's Map type is entry-written: a copy of it copies its buckets too, so
the cell's copy-if-shared function is deep; it runs only when the cell's
value is shared.

Counted receipt: allocations of one `Bump` on a store of K buckets, from
the Go of the program built by the compiler before the rule (base
e02194103), at the entry rule (MAPCOPY round 1, 983a6a13a) and with the
move-out (scratch harness, Go's `runtime.MemStats`).

| store | base | entry rule | move-out |
| --- | --- | --- | --- |
| 500 buckets | 3015 allocations, 426377 B | 9 allocations, 1072 B | 5 allocations, 560 B |
| 1000 buckets | 6017 allocations, 851378 B | 9 allocations, 1072 B | 5 allocations, 560 B |

Copy sites in `Bump`'s Go: 3 at base (the whole store, entry by entry, at
the `Get`; the bucket, deep; the cell's copy, entry by entry), 2 at the
entry rule (the bucket, a copy of its two Maps; the cell's copy), 1 with
the move-out: the cell's copy-if-shared function.
