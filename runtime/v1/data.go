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
