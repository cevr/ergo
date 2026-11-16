// Package selfhostcheck holds the agreement properties of the compiler
// written in Ergo (capstone S1): the formatter of compiler/format, linked
// into the Go module as internal/linked/format, runs in process beside the
// Go original, which stays the oracle. internal/hostsyntax encodes the Go
// parser's tree for it. No production code imports this package.
package selfhostcheck
