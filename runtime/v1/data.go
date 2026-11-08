package v1

// The bodies of slices.WithCapacity and of the prelude's slice and Map
// functions (docs/spec/stdlib.md, Slices and Maps); the rest of std slices
// is in collections.go. A slice is a Go slice
// and a Map a Go map; aliasing follows Go in v1 (check-v1 Q2; #222 and
// #197 own the change). The prelude's Get returns Option[V], a type the
// lowering emits in each program, so the lowering writes its body in the
// program, with the read this file's Get is.
//
// Append, Get and Delete are marked //ergo:pure (docs/spec/stdlib.md, Pure
// bodies 5; lw-pure-prelude): each computes on its arguments with a Go
// built-in alone, so a call of one is transparent in a footprint, and a
// function that only loops, does arithmetic and calls them takes no
// environment and polls no task. Their writes are those Go's append and
// delete make to the slice's array and to the Map, which aliasing in v1
// already allows in a pure frame (an index write does the same).

// WithCapacity is slices.WithCapacity[T](n): an empty slice with room for
// n elements. It fabricates no element (parse-v1 ruling 7 withdrew
// slice.Repeat). A negative n is a defect, Go's own makeslice panic.
func WithCapacity[T any](n int) []T { return make([]T, 0, n) }

// Append is the prelude's xs.Append(x): xs with x added at the end. Like
// Go's append, the result may share xs's array.
//
//ergo:pure
func Append[T any](xs []T, x T) []T { return append(xs, x) }

// Get is the read behind the prelude's m.Get(k): the value of k in m and
// whether m has the key, Go's comma-ok form. The lowering writes the
// prelude's Get in each program as this read, Some of the value or None
// (lower, intrinsic), so this body is the reviewed purity fact of that
// read and its Go reference.
//
//ergo:pure
func Get[K comparable, V any](m map[K]V, k K) (V, bool) {
	v, ok := m[k]
	return v, ok
}

// Delete is the prelude's m.Delete(k): m without the key k. It changes m
// itself, as Go's delete does, and returns it.
//
//ergo:pure
func Delete[K comparable, V any](m map[K]V, k K) map[K]V {
	delete(m, k)
	return m
}
