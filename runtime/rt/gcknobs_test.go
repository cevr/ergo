package rt

import (
	"runtime/debug"
	"testing"
)

// TestGCKnobsMatchRuntimeDebug: each knob that gcknobs.go binds by name
// sets what runtime/debug's wrapper reads back, and returns the previous
// setting as the wrapper does (SIZE-HELLO). A binding to the wrong runtime
// function, or a result dropped in the conversion, reads another value.
// Mutant: size-hello-knob-swapped.
func TestGCKnobsMatchRuntimeDebug(t *testing.T) {
	oldPercent := debug.SetGCPercent(137)
	defer debug.SetGCPercent(oldPercent)
	if got := gcPercent(211); got != 137 {
		t.Errorf("gcPercent returned %d, want the previous percent 137", got)
	}
	if got := debug.SetGCPercent(137); got != 211 {
		t.Errorf("runtime/debug reads the percent %d after gcPercent(211)", got)
	}

	oldLimit := debug.SetMemoryLimit(-1)
	defer debug.SetMemoryLimit(oldLimit)
	debug.SetMemoryLimit(3 << 30)
	if got := memoryLimit(-1); got != 3<<30 {
		t.Errorf("memoryLimit(-1) read %d, want 3 GiB", got)
	}
	if got := memoryLimit(5 << 30); got != 3<<30 {
		t.Errorf("memoryLimit returned %d, want the previous 3 GiB", got)
	}
	if got := debug.SetMemoryLimit(-1); got != 5<<30 {
		t.Errorf("runtime/debug reads the limit %d after memoryLimit(5 GiB)", got)
	}

	oldFault := debug.SetPanicOnFault(false)
	defer debug.SetPanicOnFault(oldFault)
	if got := panicOnFault(true); got {
		t.Errorf("panicOnFault returned %v, want the previous false", got)
	}
	if got := debug.SetPanicOnFault(false); !got {
		t.Errorf("runtime/debug reads panic-on-fault %v after panicOnFault(true)", got)
	}
}
