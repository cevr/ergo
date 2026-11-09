# lru

A generic least-recently-used cache, a memo built on it, and a small price-quoting program that uses both. It is the cache a Go programmer writes (a hash map into a doubly linked list), in Ergo.

```sh
printf 'apple\npear\napple\n' | lru            # quote through a memo of 2 entries
printf 'apple\npear\napple\n' | lru quote 3    # ... of 3 entries
lru bench 1000000                              # 1e6 cache operations, prints a checksum
```

## Layout

- `cache/`: `Cache[K comparable, V]`, the generic LRU. `cache.ergo` holds `New` (fails `BadCapacity`), `Get` (touches), `Peek` (does not), `Put` (returns the evicted `Entry` as an `Option`), `Remove`, `Entries` (newest first) and `Sorted[K ordered, V]` (key order). The list lives in an index arena (`[]node`, with `prev` and `next` indexes and a free list through `next`), since Ergo has no pointer fields; mutation goes through a `*Cache` borrow. `laws.ergo` and `model.ergo` hold the laws; `model.ergo` is a slow reference model (a slice, newest first) for the law `ModelAgrees`.
- `memo/`: `Memo[K, V]` remembers the results of a function in a `Cache`, and counts hits and misses. `Get[K, V, E, R](m *Memo[K, V], k K, load func(k K) V fails E needs R) V fails E needs R` passes on what the loader fails with and what it needs. It does not remember a failure.
- `catalog/`: the demo's domain. The operation type `Catalog` (`Price(sku) int fails Unknown`) carries the laws `Stable` (two lookups agree, which is what makes a memo in front of it sound) and `NotNegative`. `Quote` looks a price up through a memo.
- `memory/`: `Table`, the in-memory `Catalog` over a parsed price list, which counts its lookups.
- `cmd/lru/main.ergo`: the binary. It binds `catalog.Catalog` to a `memory.Table` and the host's standard input, and parses its arguments into the union `Command = Quote | Bench`.

## What it shows

- **Generics with constraints.** `Cache[K comparable, V]`; `Sorted[K ordered, V]` needs ordering only where it sorts.
- **Typed failures through a generic wrapper.** `memo.Get` has the rows of its `load`: quoting fails `catalog.Unknown` and needs `catalog.Catalog`, and the memo itself names neither.
- **Laws.** `cache/laws.ergo`: `CapacityBound`, `GetAfterPut`, `MostRecentKept`, `EvictsTheLeastRecent`; `cache/model.ergo`: `ModelAgrees` (60 seeded operations agree with the reference model, outcome by outcome and entry by entry); `memo/laws.ergo`: `Transparent`, `CountsAddUp`, `RemembersAfterMiss`, `FailureIsNotRemembered`; `catalog.Catalog`: `Stable`, `NotNegative`. The cache and memo laws run as they are, each over the generated keys and over a seeded run of up to 24 keys that meet again (`KeysOf`); `catalog.Catalog`'s laws run in `memory` against `Table`, built by `NewTable` from generated prices (G-exup-1, closed in batch 13); `NewTable` drops a negative price, which the law `NotNegative` found once it ran.
- **Exhaustive match.** `Option` results everywhere a Go LRU returns `(V, bool)`; the model's `Op` and `Outcome` unions.

## Cases

`quote` (memo of 2: one hit, an eviction, an unknown SKU), `quote-three` (memo of 3: three hits), `bench` (20,000 operations; the checksum equals the hand-written Go control's), `usage` and `bad-count` (exit 2 with the usage on standard error).

## Benchmark

`lru bench N` runs N get-or-put operations on a cache of 1,024 entries over 4,096 keys. The hand-written Go control (a map into a pointer list, the shape of `hashicorp/golang-lru`'s `simplelru`) and its command are in the run dir's `bench-pending-ex-f.md`; both print the same checksum.

## Limits found

New gaps (`gaps-ex-f.md`):

- G-ex-f-1: no slice of a slice (`xs[i:j]`), so a free stack of indexes cannot pop; the free list is linked through the slots instead.
- G-ex-f-2: a generic operation type cannot be a capability (`needs Source[K, V]`), so the memo takes its loader as a function and the law that makes memoizing sound (`Catalog.Stable`) lives in the domain, not in the memo.
- G-ex-f-3: a std extern (`strings.ToUpper`) is not a function value, and the message says it is "called directly".
- G-ex-f-4: a type parameter has no zero value, so a removed entry's value cannot be cleared for the garbage collector.
- G-ex-f-5: a non-generic function on an instance (`Apply(c *Cache[int, int], …)`) is not a dot call, and the message does not say why.
- G-ex-f-6: `match m.Get(k)` builds an `Option` interface and a type switch where Go writes comma-ok, and a function that reads a Map takes the environment and polls the task.

Closed in batch 12: G-ex-b-3 (`Table` counts lookups in a `sync.Ref`; the old Map counter wrote through a copy under the value rule and counted nothing), G-ex-d-10 (`main` holds the commands and returns from inside its `with`). Also hit: G-ex-b-18, G-ex-c-11 (`number` is a prelude name), G-ex-d-13 (laws at `int`), G-exup-1.
