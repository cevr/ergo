# gointerop

A checksum and statistics tool written to lean on Go's standard library from Ergo, and a record of how far that goes today. `sum` and `check` print what `sha256sum` and `sha256sum -c` print; `stats` summarizes numbers.

```sh
ergo run cmd/gointerop -- sum a.txt b.txt   # "DIGEST  NAME" per file; no file means standard input, "-"
ergo run cmd/gointerop -- check list        # "NAME: OK" or "NAME: FAILED" per line of list; exit 1 on a failure
printf '2 4 4 4 5 5 7 9' | ergo run cmd/gointerop -- stats
```

## What a Go programmer would write, and what this program does

A Go version imports `crypto/sha256`, `encoding/hex` and `bufio`, and maps `strconv`'s errors. In Ergo, `import go` is not built yet (#202); std binds the pure Go packages, and a user extern binds a function of the pure allowlist (`math`, `path/filepath`, `slices`, `sort`, `strconv`, `strings`, `unicode/utf8`, `encoding/hex`). So:

- **SHA-256 is Go's** (`sha256/`): the provider is `hex.Encode(sha256.Sum(text))`, std `sha256` and `hex` over Go's `crypto/sha256` and `encoding/hex`. It replaced a FIPS 180-4 SHA-256 written in Ergo (0.75 s on 50 MB where Go's assembly takes 0.08 s); the FIPS vectors and coreutils' block-edge digests stay as its tests.
- **The statistics use Go's `math`** (`stats/`): `math.Sqrt` and `math.Abs` are std; `math.Log`, `math.Exp` and `strings.TrimRight`, which std lacks, are user externs.
- **Go's errors arrive typed where std adapts them**: `stats.Read` reads numbers with std `strconv.ParseFloat`, whose Go `*NumError` is two failures, `Malformed` and `OutOfRange`; `Read` turns them into `NotANumber{Line, Text}` and `TooLarge{Line, Text}`. A user extern of a Go function that returns an `error` now declares `fails go.Error` (its `Text` is the error's message); this program has no such extern: std's typed failures say more.
- **Extern functions are values**: a user or std extern can be passed where a function is expected (a Go callback of an unnamed function type).

## Layout

- `checksum/`: the domain. The operation type `Hasher` (`Sum`, `Name`) with the laws `Deterministic`, `FixedWidth` and `Distinguishes`; list entries (`Entry`, `Parse` fails `BadLine`, `Line`); `Verdict = Matches | Differs{Got} | Missing`, `Check` (needs `Hasher`) and `Report`.
- `sha256/`: `Hasher`, the SHA-256 provider of `checksum.Hasher`, and its tests (vectors and block edges; `ergo test cmd/gointerop` runs the `Hasher` laws against it).
- `stats/`: `Read`, `Summarize` (Welford's update; fails `Empty`), `Text`, `Lines`; the laws `MeanIsBetween`, `ShiftKeepsSpread`, `GeoMeanAtMostMean`.
- `cmd/gointerop/main.ergo`: the binary; it binds `checksum.Hasher` to `sha256.Hasher`, `fs.Files` and the standard input to the host. Its tests run on an `fstest.Memory` disk.
- `testdata/`: `a.txt`, `b.txt` and a `list` with one good, one wrong and one missing entry.

## Cases

`sum-stdin`, `sum-files` (digests equal `sha256sum`'s), `check` (OK, FAILED, FAILED open or read; exit 1), `stats`, `stats-bad` (`1e999` is too large, exit 1), `usage` (exit 2). `check` counts unreadable files with the mismatches in its warning, where `sha256sum` reports them on a line of their own.

## Limits found

- G-ex-f-10 (narrowed): still no `import go`, no `[]byte`, no callback of a named function type, and a failure row at an extern is `go.Error` only. Closed: SHA-256 and hex are std, extern functions are values, `fails go.Error`.
- G-exup-1 (closed in batch 13): the `checksum.Hasher` laws run against `sha256.Hasher` in `cmd/gointerop`, the least package that sees both, so the tests that restated them are gone.

Closed since ex-f: G-ex-f-11 (`math/bits` and `encoding/hex` are std), G-ex-f-12 (`fstest.Memory`), G-ex-d-5 (`Command` has `==`), G-ex-d-10 (`main` returns from its `with`). Also hit before: G-ex-b-18 (a package-level `Map` makes a "pure" hash nondeterministic, which a mutant uses).
