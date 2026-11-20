# wtcache

A write-through cache of text settings. `set KEY VALUE` writes the value through to a backing as one line, the value quoted by `strconv.Quote`. Then it keeps, in the cache, the value that the backing's line decodes to. `get KEY` reads the cache alone. `load KEY` fills the cache from the backing when the cache holds no value. `writes` counts the lines written.

## Layout

- `settings`: the domain. It holds the operation type `Backing` with its law `ReadsTheLastWrite`, the codec `Encode` and `Decode` with the law `RoundTrips`, and the `Cache`.
- `memory`: `Lines`, a `Backing` in a map, and the tests that run the law and the write-through against it.
- `cmd/wtcache`: the binary, the only place that chooses the backing.
- `cmd/wtbench`: the workload of the receipt `benchmarks/laws/encdec/count.sh [CHECKOUT] [RUNS]`. It counts this program built with and without rewrites against hand Go and the floor (`benchmarks/laws/encdec/hand`).

## What it shows: a law that sees through functions

`Set` reads back what it wrote:

```ergo
let line = Encode(value)
Backing.Write(key, line)
let kept = try Decode(line)
```

The round trip `Decode(Encode(value))` spans two functions and a let. The build matches it to the std law `strconv.UnquoteInvertsQuote` (`{ try Unquote(Quote(s)) } == { s }`), whose validated entry is in std's shipped ledger. To match it, the build unfolds `Decode` and `Encode`, each a pure function of one expression, and the let `line`. It then replaces the decode with `value` itself. The quote still runs, because the backing needs the line. Only the parse back is gone, and with it the failure that cannot happen. `ergo inspect rewrites ./cmd/wtcache` lists the site:

```
settings/settings.ergo:49:17  strconv.UnquoteInvertsQuote  validated by eval  …  current  applied
```

`Load`'s decode of a line read from the backing is not a round trip, and it stays a decode.
