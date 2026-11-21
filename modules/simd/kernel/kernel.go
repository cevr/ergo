// Package kernel holds the Go bodies of the scan operations of package
// scan (U1 SIMD-SCAN; docs/decisions.md, the 2026-11-02 SIMD rule and its
// 2026-11-18 and 2026-11-19 lines). Each is bound by an extern of scan,
// this module's own Go package (docs/spec/interop.md, Trusted Go 3), and a
// rewrite law there whose left side is scan's Ergo reference and whose
// right side is the kernel: once `ergo test` validates it, the build calls
// the kernel wherever the reference is called (Trusted Go 5, pure by a law).
//
// Two builds. With GOEXPERIMENT=simd on amd64 (vector_amd64.go) each
// kernel scans 32 bytes at a time with simd/archsimd's AVX2 vectors on a
// CPU that has them, then the tail byte by byte. In every other build
// (stock.go) each is the scalar loop: a plain go build is correct with no
// experiment (STOCK GO STAYS CORRECT). arm64 Neon is a follow-up:
// archsimd's Mask8x16 has no ToBits in go1.27.4.
//
// Each kernel takes the bytes as a []uint8 and returns what its reference
// returns; it imports only effect-free packages (LF2m's PureImports) and
// no unsafe. No vector type crosses the package's API.
package kernel
