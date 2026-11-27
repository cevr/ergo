# tsffi

Tag: `default`. The library side of Ergo's in-process TypeScript bridge (`compiler/tsffi`, native backend unit NB-3): a small geometry package whose exported surface is the bridge's first contract, and the strict `.d.ts` the bridge emits for it.

- `geo/geo.ergo` is the library: a nominal `Meters`, records `Point`, `Circle`, `Polygon`, the closed union `Shape`, the failures `Degenerate` and `NegativeRadius` of `Area`'s fails row, an optional `Centroid`, a `Scale` that writes through `mut []Point`, and `Checksum` over bytes. Its laws: a square's area is its side squared; scaling the points scales their centroid.
- `cmd/tsffi` measures shapes from the command line (`tsffi area circle R`, `tsffi area polygon X,Y...`, `tsffi centroid X,Y...`, `tsffi checksum TEXT`); a typed failure exits 1 and names the failure.
- `testdata/geo.d.ts` is what the bridge declares for TypeScript callers of `geo`: `Meters` as a branded `number`, each record a readonly interface tagged `_tag`, `Shape` as the union of its members, 64-bit integers as `bigint`, an option as `T | undefined`, a read-only slice as `readonly T[]` and a `mut` one as `T[]`, and `Area` as `Result<Meters, Degenerate | NegativeRadius>`.

`testdata/geo.d.ts` is derived: only the emitter writes it, and its check fails when the file is not what the emitter writes. From `compiler/`:

```sh
ERGO_FALLBACK=off ../bin/ergo run ./tsffi/emit -- ../examples/tsffi/testdata          # write
ERGO_FALLBACK=off ../bin/ergo run ./tsffi/emit -- --check ../examples/tsffi/testdata  # drift check
```

The emitter reads the contract `tsffi.GeoContract`, written by hand to match `geo/geo.ergo` until the bridge reads contracts from the checked package summary; the `bun:ffi` and Node N-API bindings that call a native `geo` come in later rounds of NB-3.
