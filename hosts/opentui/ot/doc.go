// Package ot binds OpenTUI's native core (libopentui, the checked ot_*
// C ABI, version 2) over purego: no cgo, no JavaScript. opentui.go is what
// the repository's C binder (internal/bindc) writes from the pinned
// header opentui.h (OpenTUI v0.9.3, commit 3dc895b) and the contract
// opentui.bind.json; never edit it by hand, run go generate here.
//
// The library itself is never in the repository: OpenTUI's npm packages
// are "MIT AND LGPL-2.1-or-later". cmd/fetch downloads it, checked by its
// pinned sha512 integrity, and Find and Open (load.go) locate and load it.
package ot

//go:generate go run ./internal/otgen/run
