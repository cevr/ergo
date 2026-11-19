# simd

An Ergo module of this repository (`modules/`): byte scans with vector bodies (U1 SIMD-SCAN; [docs/decisions.md](../../docs/decisions.md), the 2026-11-02 SIMD rule and its 2026-11-18 and 2026-11-19 lines).

| Package | What |
| --- | --- |
| `scan` | `IndexByte`, `IndexAny2`, `IndexOutside`, `CountByte` over a string. Each has its Ergo reference, a plain byte loop that is its specification, and a law beside it that holds the operation to the reference. |
| `kernel` (Go) | The vector bodies, bound by `scan` through the door of the module's own Go bodies (docs/spec/interop.md, Trusted Go 3). `vector_amd64.go` (`goexperiment.simd && amd64`) uses `simd/archsimd`'s AVX2 vectors; `stock.go` answers `Enabled() == false` in every other build, so a plain `go build` runs the Ergo reference. |

Another program takes it once a module's Go body may be bound from another module (LAWFFI LF2m: a body with no effects whose law validates). Nothing in the compiler names this module: any module may ship kernels the same way.

## Builds

- `ergo test ./scan` runs the laws in the stock build (the reference against itself, and the module's wiring).
- `GOEXPERIMENT=simd ergo test ./scan` runs them against the AVX2 kernels. `GOEXPERIMENT` is read from the environment and enters Go's build identity; the direct build path refuses a set `GOEXPERIMENT`, so such a build takes `go build`.

## Decided

- The kernels take a string and view its bytes in place for the vector loads (`kernel.load`), with the safety invariant named in a comment: every load is inside the string, and nothing is stored. A copy into a `[]byte` would cost more than the scan. Reverse by: taking `[]byte` once a module body admits slices (LAWFFI LF2m).
- `kernel.TestNoReadPastTheEnd` (vector build, linux) puts each text at the end of a page whose next page is unreadable, so a read one byte past the end faults. Run it with `GOEXPERIMENT=simd go test ./kernel` in this folder.
- arm64 Neon is not built: go1.27.4's `archsimd.Mask8x16` has no `ToBits`. arm64 builds take `stock.go`.
- A kernel is not pure yet: a module body's purity is earned by its law once LF2m lands (2026-11-19 ruling). Until then each call is opaque in a footprint.
