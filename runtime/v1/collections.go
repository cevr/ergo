package v1

// The Go bodies of the std packages slices and cmp (docs/spec/stdlib.md,
// Slices and Maps; std-align S2a). Each calls Go's function of the same
// name. A function that sorts or reverses in Go returns a new slice here
// and leaves its argument unchanged, because an Ergo let is immutable and
// aliasing is visible (std-align plan, owner question 3). cmp's Compare and
// Less and slices' Contains, Index, Equal, Max, Min and BinarySearch are
// Ergo bodies (NP4f-3).
//
// This file imports only slices, so its pure bodies stay inside
// PureImports.
//
// Prior art: Go's slices and cmp packages, which these call or restate;
// Gleam's gleam/list binds Erlang's lists:sort the same way, as a direct
// external.

import "slices"

// ordered is Ergo's ordered constraint (types.md, Generics 2): the integer,
// float and string types and the types defined over them, duration
// (time.Duration) among them. Its terms are those of Go's cmp.Ordered, so
// the two admit the same type arguments, and a lowered Ergo type parameter
// constrained by ordered (the lowering's generated type set) satisfies it.
type ordered interface {
	~int | ~int8 | ~int16 | ~int32 | ~int64 | ~uint | ~uint8 | ~uint16 | ~uint32 | ~uint64 | ~uintptr | ~float32 | ~float64 | ~string
}

// SlicesSort is slices.Sort[T](xs): a sorted copy of xs, in ascending
// order. A float NaN sorts first, as in Go. Where xs is consumed (no
// binding reads it after the call), the lowering calls its owned form
// SlicesSortOwned, which sorts xs in place.
//
//ergo:pure
//ergo:owned SlicesSortOwned
//ergo:wait none
func SlicesSort[T ordered](xs []T) []T {
	out := slices.Clone(xs)
	slices.Sort(out)
	return out
}

// SlicesSortOwned sorts xs in place and returns it: SlicesSort's owned
// form, called on a consumed slice, and slices.Sorted's, on the slice it
// has just collected, which nothing else holds. It writes its argument,
// so it is not marked pure.
//
//ergo:wait none
func SlicesSortOwned[T ordered](xs []T) []T {
	slices.Sort(xs)
	return xs
}

// SlicesSortFunc is slices.SortFunc(xs, compare): a copy of xs sorted by
// compare, which returns a negative number, zero or a positive number. The
// order of equal elements is not kept.
//
//ergo:wait none
func SlicesSortFunc[T any](xs []T, compare func(a T, b T) int) []T {
	out := slices.Clone(xs)
	slices.SortFunc(out, compare)
	return out
}

// SlicesSortStableFunc is slices.SortStableFunc(xs, compare): a copy of xs
// sorted by compare, equal elements in their order in xs.
//
//ergo:wait none
func SlicesSortStableFunc[T any](xs []T, compare func(a T, b T) int) []T {
	out := slices.Clone(xs)
	slices.SortStableFunc(out, compare)
	return out
}

// SlicesReverse is a copy of xs in the reverse order. Where xs is
// consumed (no binding reads it after the call), the lowering calls its
// owned form SlicesReverseOwned, which reverses xs in place.
//
//ergo:pure
//ergo:owned SlicesReverseOwned
//ergo:wait none
func SlicesReverse[T any](xs []T) []T {
	out := slices.Clone(xs)
	slices.Reverse(out)
	return out
}

// SlicesReverseOwned reverses xs in place and returns it: SlicesReverse's
// owned form, called on a consumed slice. It writes its argument, so it is
// not marked pure.
//
//ergo:wait none
func SlicesReverseOwned[T any](xs []T) []T {
	slices.Reverse(xs)
	return xs
}

// SlicesClone is slices.Clone(xs): a copy of xs in a new array, so a write
// to it does not reach xs. slices.Items returns it, so a caller cannot
// unsort an Ascending (SS5).
//
//ergo:pure
//ergo:wait none
func SlicesClone[T any](xs []T) []T { return slices.Clone(xs) }

// SlicesConcat is slices.Concat(a, b): a new array of at least len(a) +
// len(b), so the result shares no array with a or b. Where a is consumed
// (no binding reads it after the call), the lowering calls its owned form
// SlicesConcatOwned, which appends b to a's array (OWN1).
//
//ergo:pure
//ergo:owned SlicesConcatOwned
//ergo:wait none
func SlicesConcat[T any](a []T, b []T) []T { return slices.Concat(a, b) }

// SlicesConcatOwned is append(a, b...): SlicesConcat's owned form, called
// on a consumed a, whose array nothing else reads, so b's elements go into
// a's spare capacity and a loop of p = slices.Concat(p, q) is linear, as
// hand Go's p = append(p, q...) is. It writes past a's length, so it is
// not marked pure. A test build calls its checking form,
// SlicesConcatPoison, instead.
//
//ergo:check SlicesConcatPoison
//ergo:wait none
func SlicesConcatOwned[T any](a []T, b []T) []T { return append(a, b...) }

// SlicesConcatPoison is SlicesConcatOwned's checking form, which a test
// build calls where the run build calls the owned form (lower/handover.go):
// it makes the new array SlicesConcat makes, then clears a's whole array,
// which the proof says no binding reads again. A read the proof missed
// reads zero values, so the program's own tests see it.
//
//ergo:wait none
func SlicesConcatPoison[T any](a []T, b []T) []T {
	out := slices.Concat(a, b)
	clear(a[:cap(a)])
	return out
}

// AppendPoison is the checking form of an Append the run build makes in
// place on a dead field (lower/handover.go): a new array, as the clipped
// Append makes, and then xs's whole array cleared, since the proof says no
// binding reads it again.
func AppendPoison[T any](xs []T, x T) []T {
	out := append(xs[:len(xs):len(xs)], x)
	clear(xs[:cap(xs)])
	return out
}

// SlicesInsert is xs with ys before index i, in a new array: Go's
// slices.Insert(xs, i, ys...) writes xs's array when it has room, so the
// body concatenates instead, and the result shares no array with xs or
// ys. An i outside 0..len(xs) panics, as Go's does: a defect.
//
//ergo:pure
//ergo:wait none
func SlicesInsert[T any](xs []T, i int, ys []T) []T { return slices.Concat(xs[:i:i], ys, xs[i:]) }

// SlicesDelete is xs without xs[i:j], in a new array: Go's
// slices.Delete(xs, i, j) shifts xs's own elements and zeroes its tail, so
// the body concatenates instead. A bound outside 0..len(xs), or an i after
// j, panics: a defect.
//
//ergo:pure
//ergo:wait none
func SlicesDelete[T any](xs []T, i int, j int) []T {
	_ = xs[i:j]
	return slices.Concat(xs[:i:i], xs[j:])
}

// SlicesRepeat is slices.Repeat(xs, n): xs n times over, in a new array.
// A negative n, or a length past int, panics, as Go's does: a defect.
//
//ergo:pure
//ergo:wait none
func SlicesRepeat[T any](xs []T, n int) []T { return slices.Repeat(xs, n) }

// SlicesFill is slices.Repeat(xs, n) written into out, which the caller
// makes with length len(xs)*n, a constant (wf10 OWN1 PA3): the caller's
// make of constant length stays on its stack when the result does not
// escape, as hand Go's `var a [12]int` does. xs is not kept. It returns
// out.
//
//ergo:pure
//ergo:wait none
func SlicesFill[T any](out []T, xs []T) []T {
	for i := 0; i < len(out); i += len(xs) {
		copy(out[i:], xs)
	}
	return out
}

// SlicesSlice is xs[i:j:j], the full slice expression: no room after j,
// so an append to the result copies and never writes xs[j]. A bound out
// of range panics: a defect, as an index out of range is.
//
//ergo:pure
//ergo:wait none
func SlicesSlice[T any](xs []T, i int, j int) []T { return xs[i:j:j] }

// MapsClone is maps.Clone(m): a new map with the keys and values of m.
// It is restated because maps is not in PureImports.
//
//ergo:pure
//ergo:wait none
func MapsClone[K comparable, V any](m map[K]V) map[K]V {
	out := make(map[K]V, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// CmpMax is Go's builtin max(a, b): the larger value; for floats a NaN
// gives NaN, and max(-0, +0) is +0.
//
//ergo:pure
//ergo:wait none
func CmpMax[T ordered](a T, b T) T { return max(a, b) }

// CmpMin is Go's builtin min(a, b): the smaller value; for floats a NaN
// gives NaN, and min(-0, +0) is -0.
//
//ergo:pure
//ergo:wait none
func CmpMin[T ordered](a T, b T) T { return min(a, b) }
