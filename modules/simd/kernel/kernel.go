// Package kernel holds the vector bodies of the scan operations of package
// scan (U1 SIMD-SCAN; docs/decisions.md, the 2026-11-02 SIMD rule and its
// 2026-11-18 and 2026-11-19 lines). Package scan binds them as bodies of
// this module's own Go package (docs/spec/interop.md, Trusted Go 3) and
// calls one only when Enabled reports true; otherwise it runs its Ergo
// reference, which the laws of package scan hold every kernel to.
//
// Two builds. With GOEXPERIMENT=simd on amd64 (vector_amd64.go) the
// kernels use simd/archsimd's 256-bit AVX2 vectors, and Enabled is
// archsimd.X86.AVX2(). In every other build (stock.go) Enabled is false,
// so the Ergo reference runs and gc drops the call: a plain go build is
// correct with no experiment (STOCK GO STAYS CORRECT). arm64 Neon is a
// follow-up: archsimd's Mask8x16 has no ToBits in go1.27.4.
//
// Each kernel takes the text as a string and returns what its reference
// returns. No vector type crosses the package's API.
package kernel
