// Package ir carries the IR build path's compiler patch and its lock
// (plan-tier §1.4) into the ergo binary, so ergo can build its patched
// compile from a pristine Go release with no checkout of this repository
// (internal/toolchain, Patch).
package ir

import "embed"

// Files holds lock and every *.patch it names.
//
//go:embed lock *.patch
var Files embed.FS
