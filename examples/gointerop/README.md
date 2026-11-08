# gointerop

A checksum and statistics tool written to lean on Go's standard library from Ergo, and a record of how far that goes today. `sum` and `check` print what `sha256sum` and `sha256sum -c` print; `stats` summarizes numbers.

```sh
gointerop sum a.txt b.txt      # "DIGEST  NAME" per file; no file means standard input, "-"
gointerop check list           # "NAME: OK" or "NAME: FAILED" per line of list; exit 1 on a failure
printf '2 4 4 4 5 5 7 9' | gointerop stats
```

## What a Go programmer would write, and what this program does

A Go version imports `crypto/sha256`, `encoding/hex` and `bufio`, and maps `strconv`'s errors. In Ergo, `import go` is not built yet (#202), and a user extern binds only a function of the pure allowlist (`math`, `path/filepath`, `slices`, `sort`, `strconv`, `strings`, `unicode/utf8`) with basic types, no callback and no failure. So:

- **SHA-256 is written in Ergo** (`sha256/`): FIPS 180-4, with `uint32` arithmetic, shifts and rotations. It matches the FIPS vectors (one million `a`s included), coreutils `sha256sum` at the block edges, and Go's `crypto/sha256` on a 50 MB file. Its hex output uses Go's `strconv.FormatUint` through a user extern.
- **The statistics use Go's `math`** (`stats/`): `math.Sqrt`, `math.Log`, `math.Exp` and `math.Abs`, and `strings.TrimRight`, which std `strings` lacks, are user externs.
- **Go's errors arrive typed only where std adapts them**: `stats.Read` reads numbers with std `strconv.ParseFloat`, whose Go `*NumError` is two failures, `Malformed` and `OutOfRange`; `Read` turns them into `NotANumber{Line, Text}` and `TooLarge{Line, Text}`.
- **No Go callback** could be passed: an extern's parameter cannot be a function.

## Layout

- `checksum/`: the domain. The operation type `Hasher` (`Sum`, `Name`) with the laws `Deterministic`, `FixedWidth` and `Distinguishes`; list entries (`Entry`, `Parse` fails `BadLine`, `Line`); `Verdict = Matches | Differs{Got} | Missing`, `Check` (needs `Hasher`) and `Report`.
- `sha256/`: `Hasher`, the SHA-256 provider of `checksum.Hasher`; `Padded`, `Words`, `Hex`; the law `PaddingFills`.
- `stats/`: `Read`, `Summarize` (Welford's update; fails `Empty`), `Text`, `Lines`; the laws `MeanIsBetween`, `ShiftKeepsSpread`, `GeoMeanAtMostMean`.
- `cmd/gointerop/main.ergo`: the binary; it binds `checksum.Hasher` to `sha256.Hasher`, `fs.Files` and the standard input to the host. Its test binds an in-memory `disk` instead.
- `testdata/`: `a.txt`, `b.txt` and a `list` with one good, one wrong and one missing entry.

## Cases

`sum-stdin`, `sum-files` (digests equal `sha256sum`'s), `check` (OK, FAILED, FAILED open or read; exit 1), `stats`, `stats-bad` (`1e999` is too large, exit 1), `usage` (exit 2). `check` counts unreadable files with the mismatches in its warning, where `sha256sum` reports them on a line of their own.

## Limits found

New gaps (`gaps-ex-f.md`):

- G-ex-f-10: no Go interop past the pure allowlist: no `import go`, no callback, no `[]byte`, no failure row at an extern. The SHA-256 written in Ergo takes 0.75 s on 50 MB where Go's assembly takes 0.08 s (rough).
- G-ex-f-11: a pure Go package off the allowlist (`math/bits`, `encoding/hex`) is said to "reach the host".
- G-ex-f-12: std `fs` has no in-memory `Files` provider for tests.

Also hit: G-ex-b-18 (a package-level `Map` makes a "pure" hash nondeterministic, which a mutant uses), G-ex-d-5, G-ex-d-10.
