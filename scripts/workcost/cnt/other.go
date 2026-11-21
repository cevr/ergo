//go:build !linux

// Command cnt counts instructions with Linux's perf_event_open; elsewhere
// it says so.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "cnt: counting instructions needs Linux (perf_event_open)")
	os.Exit(2)
}
