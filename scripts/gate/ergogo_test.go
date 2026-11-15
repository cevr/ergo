package main

import (
	"os"
	"testing"
)

// TestStepsBuildWithTheSystemGo: a step's ergo builds with the go command
// on PATH (ERGO_GO=system) unless the caller chose another, so no step
// downloads the pinned Go release (internal/toolchain) from the network.
func TestStepsBuildWithTheSystemGo(t *testing.T) {
	t.Setenv(ergoGo, "")
	os.Unsetenv(ergoGo)
	if value, _ := environmentVariable(keyedEnvironment(), ergoGo); value != "system" {
		t.Errorf("with ERGO_GO unset a step gets ERGO_GO=%q, want system", value)
	}
	t.Setenv(ergoGo, "managed")
	if value, _ := environmentVariable(keyedEnvironment(), ergoGo); value != "managed" {
		t.Errorf("with ERGO_GO=managed a step gets ERGO_GO=%q, want the caller's", value)
	}
}
