package main

import (
	"os"
	"runtime/debug"
)

// compilerGCPercent is the collector's target for the compiler's own heap
// when GOGC is unset (CHK1b). A command is short and its live heap is
// small (a check of an example holds about 15 MB), so the default target
// of 100 spends a quarter of a check's CPU in the collector; 400 trades a
// larger peak heap for it, as Go's own compiler raises its GC target.
// Receipt: docs/receipts/check-speed.md (CPU and peak memory on every
// example target). The programs ergo starts keep their own environment.
const compilerGCPercent = 400

// tuneGC sets the compiler's GC target unless the environment sets GOGC.
func tuneGC() {
	if _, set := os.LookupEnv("GOGC"); !set {
		debug.SetGCPercent(compilerGCPercent)
	}
}
