package rt

import _ "unsafe" // go:linkname

// The three collector and fault settings the runtime changes, bound to the
// Go runtime's own functions without importing runtime/debug (SIZE-HELLO).
// runtime/debug imports fmt, and through it reflect, whose package
// initializer every program then links (internal/sizes fact "^reflect\.");
// its exported wrappers only convert these results. The Go runtime pushes
// each of them under the runtime/debug name (//go:linkname in
// runtime/mgcpacer.go and runtime/rdebug.go), so the
// pull below needs no -checklinkname flag. TestGCKnobsMatchRuntimeDebug
// holds each to its runtime/debug wrapper.

//go:linkname setGCPercent runtime/debug.setGCPercent
func setGCPercent(percent int32) int32

//go:linkname setMemoryLimit runtime/debug.setMemoryLimit
func setMemoryLimit(limit int64) int64

//go:linkname setPanicOnFault runtime/debug.setPanicOnFault
func setPanicOnFault(enabled bool) bool

// gcPercent is runtime/debug.SetGCPercent: it sets the collector's percent
// and returns the previous one; a negative percent turns the collector off.
func gcPercent(percent int) int { return int(setGCPercent(int32(percent))) }

// memoryLimit is runtime/debug.SetMemoryLimit: it sets the soft memory
// limit and returns the previous one; a negative limit only reads it.
func memoryLimit(limit int64) int64 { return setMemoryLimit(limit) }

// panicOnFault is runtime/debug.SetPanicOnFault for the calling goroutine,
// returning the previous setting.
func panicOnFault(enabled bool) bool { return setPanicOnFault(enabled) }
