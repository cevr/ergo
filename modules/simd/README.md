# simd

An Ergo module of this repository (`modules/`): byte scans with vector bodies (U1 SIMD-SCAN; [docs/decisions.md](../../docs/decisions.md), the 2026-11-02 SIMD rule and its 2026-11-18 and 2026-11-19 lines).

| Package | What |
| --- | --- |
| `scan` | `IndexByte`, `IndexAny2`, `IndexOutside`, `CountByte` over a `[]byte`. Each is its Ergo reference, a plain byte loop that is its specification. Beside each sit a rewrite law, the operation `==` its kernel, which `ergo test`'s licence run validates so the build calls the kernel (docs/spec/interop.md, Trusted Go 5), and a law that holds the kernel to the reference with the needle at every offset of the vector loop and its tail. |
| `kernel` (Go) | The kernels, bound by `scan` through the door of the module's own Go bodies (Trusted Go 3). `vector_amd64.go` (`goexperiment.simd && amd64`) scans 32 bytes at a time with `simd/archsimd`'s AVX2 vectors on a CPU that has them; `stock.go` is the scalar loop in every other build, so a plain `go build` is correct. |

Nothing in the compiler names this module: any module may ship kernels the same way.

## Builds

- `ergo test ./scan` runs the laws and the licence run in the stock build; `ergo.laws` holds the validated entries.
- `GOEXPERIMENT=simd ergo test ./scan` runs them against the AVX2 kernels. `GOEXPERIMENT` is read from the environment or the go env file; it selects the kernel file the checker reads and is in every cache key.
- `GOEXPERIMENT=simd go test ./kernel` runs `TestNoReadPastTheEnd`: each slice ends where the next page is unreadable and its capacity runs on into it, so a read one byte past the length faults.

## Decided

- The kernels take `[]uint8` and load with `archsimd.LoadUint8x32(s[i : i+32])`: no `unsafe`, and every import is effect-free (LF2m's PureImports, `simd/archsimd` among them), so a validated law makes each pure.
- Each kernel checks `archsimd.X86.AVX2()` itself and falls back to the scalar loop: there is no `Enabled` extern.
- arm64 Neon is not built: go1.27.4's `archsimd.Mask8x16` has no `ToBits`. arm64 builds take `stock.go`.
- `ergo.laws`'s entries are keyed by the kernel file of the build that validated them (Foreign keys): the stock build's entries are committed; a vector build validates its own on its first `ergo test`.
