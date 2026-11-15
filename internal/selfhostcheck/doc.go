// Package selfhostcheck holds the tests of the self-hosting pilot (wf10
// SH0 to SH2): Ergo code that reimplements a compiler package runs beside
// the Go original, and a property compares the two through the exported
// Go ABI of an `ergo lib` library. The pilot's Ergo module is
// testdata/selfhost: a Go adapter (hostsyntax) exports the Go parser's
// facts in a length-prefixed encoding, and the Ergo packages decode it (tree)
// and export the library (formatapi). No production code imports this
// package.
package selfhostcheck
