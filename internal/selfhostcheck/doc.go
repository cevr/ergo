// Package selfhostcheck holds the agreement properties of the compiler
// written in Ergo (capstone S1, S2): the formatter of compiler/format,
// linked into the Go module as internal/linked/format, runs in process
// beside the Go original, which stays the oracle. The Ergo formatter
// parses with compiler/syntax and reads its rows; only the source and the
// text cross (internal/hostsyntax). No production code imports this
// package.
package selfhostcheck
