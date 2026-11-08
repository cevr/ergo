package v1

// The bodies of slices.WithCapacity and of the prelude's slice and Map
// functions (docs/spec/stdlib.md, Slices and Maps); the rest of std slices
// is in collections.go. A slice is a Go slice
// and a Map a Go map; aliasing follows Go in v1 (check-v1 Q2; #222 and
// #197 own the change). The prelude's Get returns Option[V], a type the
// lowering emits in each program, so its body is lowered at the call, not
// here.

// WithCapacity is slices.WithCapacity[T](n): an empty slice with room for
// n elements. It fabricates no element (parse-v1 ruling 7 withdrew
// slice.Repeat). A negative n is a defect, Go's own makeslice panic.
func WithCapacity[T any](n int) []T { return make([]T, 0, n) }

// Append is the prelude's xs.Append(x): xs with x added at the end. Like
// Go's append, the result may share xs's array.
func Append[T any](xs []T, x T) []T { return append(xs, x) }

// Delete is the prelude's m.Delete(k): m without the key k. It changes m
// itself, as Go's delete does, and returns it.
func Delete[K comparable, V any](m map[K]V, k K) map[K]V {
	delete(m, k)
	return m
}

// UnreachableDefect is the defect the prelude's Unreachable(reason)
// raises: a panic value, never a returned error, so no catch of typed
// failures sees it (docs/spec/rows.md, Catch 6). At is the call's source
// position, file:line:column; it is empty only for a call that did not
// come through the lowering.
type UnreachableDefect struct {
	Reason string
	At     string
}

// String is the defect's text, which the entry's report writes after
// "panic: ".
func (d *UnreachableDefect) String() string {
	if d.At == "" {
		return "unreachable: " + d.Reason
	}
	return "unreachable at " + d.At + ": " + d.Reason
}

// Unreachable is the Go body that the prelude's Unreachable binds. The
// lowering never calls it: each call site panics with UnreachableAt, which
// adds the call's position, or calls Never in a value position.
//
//ergo:pure
func Unreachable(reason string) { panic(&UnreachableDefect{Reason: reason}) }

// UnreachableAt is the defect of Unreachable(reason) at the source position
// at. A statement or a tail lowers to panic(UnreachableAt(reason, at)), the
// Go statement a hand-written Go program writes, which Go also counts as a
// terminating statement.
func UnreachableAt(reason, at string) *UnreachableDefect {
	return &UnreachableDefect{Reason: reason, At: at}
}

// Never is Unreachable(reason) at at in a position that expects a value of
// type T: it panics with UnreachableAt and returns no value.
func Never[T any](reason, at string) T { panic(UnreachableAt(reason, at)) }
