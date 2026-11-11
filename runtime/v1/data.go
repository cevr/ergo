package v1

import "slices"

// The bodies of slices.WithCapacity and of the prelude's slice and Map
// functions (docs/spec/stdlib.md, Slices and Maps); the rest of std slices
// is in collections.go. A slice is a Go slice
// and a Map a Go map, but Ergo's slices and Maps are values (owner q26,
// 2026-11-08; types.md, Bindings): the lowering hands these bodies a
// source they may write, the place of `p = p.Append(x)` or
// `p = p.Delete(k)`, `p = p.Put(k, v)` or a dead local, and otherwise a clipped slice or a
// copy of the Map (lower, values.go rule V3). The prelude's Get returns Option[V], a type the
// lowering emits in each program, so the lowering writes its body in the
// program, with the read this file's Get is.
//
// Append, Get, Delete and Put are marked //ergo:pure (docs/spec/stdlib.md, Pure
// bodies 5; lw-pure-prelude): each computes on its arguments with a Go
// built-in alone, so a call of one is transparent in a footprint, and a
// function that only loops, does arithmetic and calls them takes no
// environment and polls no task. Their writes are those Go's append and
// delete make to the slice's array and to the Map, and the lowering gives
// them only a source no other binding sees, so equal arguments give equal
// results.

// WithCapacity is slices.WithCapacity[T](n): an empty slice with room for
// n elements. It fabricates no element (parse-v1 ruling 7 withdrew
// slice.Repeat). A negative n is a defect, Go's own makeslice panic.
func WithCapacity[T any](n int) []T { return make([]T, 0, n) }

// ArenaValue is the physical storage of arena's private stored[T]. It is
// not a public Ergo type: only arena can name or obtain a cell. Its zero
// is allowed only in a freed slot, which no live Ref permits Get to read.
type ArenaValue[T any] struct{ Value T }

// ArenaTake removes the value from an arena storage cell and clears it.
// The caller validates the Ref, advances its generation, and puts the
// slot on the free list before returning; no callback can observe the
// intermediate slot. Put initializes the cell before publishing a Ref.
// This boundary keeps host zero storage private, not a zero[T]() value
// that could bypass Ergo's initialization or construction authority.
//
//ergo:pure
func ArenaTake[T any](s *ArenaValue[T]) T {
	v := s.Value
	var zero T
	s.Value = zero
	return v
}

// Append is the prelude's xs.Append(x): xs with x added at the end. Like
// Go's append, the result may share xs's array; the lowering passes a
// clipped xs where xs is still seen elsewhere.
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
// itself, as Go's delete does, and returns it; the lowering passes a copy
// of m where m is still seen elsewhere.
//
//ergo:pure
func Delete[K comparable, V any](m map[K]V, k K) map[K]V {
	delete(m, k)
	return m
}

// Put is the prelude's m.Put(k, v): m with k set to v. It changes m
// itself, as Go's index write does, and returns it; the lowering passes a
// copy of m where m is still seen elsewhere.
//
//ergo:pure
func Put[K comparable, V any](m map[K]V, k K, v V) map[K]V {
	m[k] = v
	return m
}

// SlicesTake moves index i out of xs and returns the value and remainder.
// A plain-data caller may keep xs (mode crossing), so its storage is not
// changed. With resource elements the caller gives xs up, and copying the
// slots moves their one ownership to the result; it creates no new owner.
// Index validation precedes allocation and every write.
//
//ergo:pure
func SlicesTake[T any](xs []T, i int) (T, []T) {
	value := xs[i]
	xs = slices.Clone(xs)
	copy(xs[i:], xs[i+1:])
	var zero T
	xs[len(xs)-1] = zero
	return value, xs[:len(xs)-1]
}

// MapTake is the native read-and-remove behind m.Take(k). The intrinsic
// wraps value and found in the program's Option and returns it beside m.
//
//ergo:pure
func MapTake[K comparable, V any](m map[K]V, k K) (V, bool, map[K]V) {
	value, found := m[k]
	rest := make(map[K]V, len(m))
	for key, value := range m {
		rest[key] = value
	}
	delete(rest, k) // Take moves the entry out of the remainder
	return value, found, rest
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
