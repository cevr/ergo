package v1

import (
	"ergo.local/prototype/runtime/rt"
)

type wideSlotKey struct {
	slot string
}

// WidePush binds val under slot on t for the lifetime of frame s and every
// child task started while it is in force, restoring the prior binding when s
// exits on any path. It performs no I/O and reads no host state.
//
//ergo:wait none
func WidePush[T any](s *Scope, t *rt.Task, slot string, val T) {
	saved := t.WithValue(wideSlotKey{slot: slot}, val)
	OnExit(s, func() { saved.End() })
}

// WideGet reads the innermost value bound under slot on t, or (zero, false)
// when none is bound. Outside a span this is one pointer load and branch with
// zero allocations.
//
//ergo:wait none
func WideGet[T any](t *rt.Task, slot string) (T, bool) {
	var zero T
	if t == nil {
		return zero, false
	}
	v := t.Value(wideSlotKey{slot: slot})
	if v == nil {
		return zero, false
	}
	got, ok := v.(T)
	if !ok {
		return zero, false
	}
	return got, true
}
