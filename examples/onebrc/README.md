# onebrc

The One Billion Row Challenge ([1BRC](https://github.com/gunnarmorling/1brc)) in Ergo: read lines `<station>;<temperature>` and print each station's minimum, mean and maximum, `{a=min/mean/max, b=...}`, sorted by station. The owner's question (2026-11-12, after [Ben Hoyt's Go write-up](https://benhoyt.com/writings/go-1brc/)): can Ergo and Go both do a billion rows in under a second?

```sh
onebrc FILE [WORKERS]   # WORKERS chunks at once; default one per core
```

## What it shows

- **Laws justify the parallel reduce.** `weather.Stats` merge by `Merge`, whose laws (`MergeAssociative`, `MergeCommutative`, `EmptyIsIdentity`) say the chunks may be merged in any grouping and order; `AddIsMergeOfOne` ties the table's in-place update to `Merge`. So the result is the same for every split, which the property in `cmd/ergo` checks at 1 to 17 workers.
- **Malformed input is a typed failure.** `weather.ParseLine` fails with `Malformed{Offset, Reason}`, and `Reason` is a closed union that `Explain` matches exhaustively. More than 10,000 stations is `TooManyStations`. Every chunk reports its first malformed line as a value (`Part`, a union of `Counted`, `Broken` and `Crowded`), so the earliest one in the file is reported whatever order the tasks end in.
- **No copy of the input.** `mapped` reads the file through `fs.MapFile`, a read-only mapping of the file as a `string`: the text is the page cache's bytes, and a station's name is a substring of it, never copied. `memory` is the provider for tests.
- **The fast path is speculative; the byte parser is the authority.** `weather.Scan` reads a station and a temperature eight bytes at a time (SWAR: Wuerthinger's and Yastrebov's methods), and hands every line it does not accept, and every line within 128 bytes of the end, to `ParseLine`.

## Layout

- `weather/`: the domain. `stats.ergo` (Stats, Merge and their laws, the mean and its text), `line.ergo` (the line's form, the failures and the byte parser), `words.ergo` (the 8-byte words), `table.ergo` (a chunk's open-addressing table and `Scan`), `summary.ergo` (the `Readings` contract, the split, the parallel scan, the merge and the report).
- `mapped/`: `Readings` over the host's files, mapped.
- `memory/`: `Readings` over texts in memory.
- `cmd/onebrc/main.ergo`: binds `mapped` over `fs.NewHost()`.

## The Go control and the speed claim

`cmd/ergo/testdata/onebrc/control` is the same algorithm in hand-optimized Go (mmap, one chunk per worker, the same table, the same words), the bar the Ergo program must meet. `cmd/ergo/testdata/onebrc/gen` writes a seeded input. `TestOneBRCAgrees` (cmd/ergo, full tier) checks that both print what a plain reference prints on seeded inputs; `TestOneBRCCountsAsHandGo` (Linux, full tier) counts their instructions per row with hardware counters and fails when Ergo runs more than the control.
