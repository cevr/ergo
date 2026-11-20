// Package opentui hosts Ergo ui programs in a terminal on OpenTUI's native
// core (github.com/anomalyco/opentui, v0.9.3), bound from Go through
// purego: one process, no JavaScript, no cgo. Package ot is the generated
// binding, screen the Go half (a terminal or a headless renderer and its
// retained scene), tui the Ergo driver that steps a ui.Session and drives
// the scene, and cmd/ the terminal binaries of the examples.
//
// It is a Go module of its own, so the Ergo module stays free of
// dependencies; the library is fetched (go run ./cmd/fetch), never
// committed.
package opentui
