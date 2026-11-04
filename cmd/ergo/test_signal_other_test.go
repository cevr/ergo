//go:build !unix

package main

import "testing"

// TestTestSignals needs POSIX signals and process groups; elsewhere it
// states its skip (the unix file holds the test).
func TestTestSignals(t *testing.T) {
	t.Skip("signal forwarding by ergo test is tested on unix hosts only: it uses the signals SIGINT and SIGTERM and process groups")
}
