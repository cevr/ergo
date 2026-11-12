package v1

// The Go bodies of the std packages slices and cmp (docs/spec/stdlib.md,
// Slices and Maps; std-align S2a). Each calls Go's function of the same
// name. A function that sorts or reverses in Go returns a new slice here
// and leaves its argument unchanged, because an Ergo let is immutable and
// aliasing is visible (std-align plan, owner question 3). Max, Min and
// BinarySearch are the Go halves of Ergo bodies that return an Option: the
// Ergo body checks the case Go answers with a panic or a second result.
//
// This file imports only slices, so its pure bodies stay inside
// PureImports. Go's cmp is not imported: CmpCompare and CmpLess restate
// cmp.Compare and cmp.Less, and TestStdCmpMatchesGo holds them to Go's.
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
func SlicesSort[T ordered](xs []T) []T {
	out := slices.Clone(xs)
	slices.Sort(out)
	return out
}

// SlicesSortOwned sorts xs in place and returns it: SlicesSort's owned
// form, called on a consumed slice, and slices.Sorted's, on the slice it
// has just collected, which nothing else holds. It writes its argument,
// so it is not marked pure.
func SlicesSortOwned[T ordered](xs []T) []T {
	slices.Sort(xs)
	return xs
}

// SlicesSortFunc is slices.SortFunc(xs, compare): a copy of xs sorted by
// compare, which returns a negative number, zero or a positive number. The
// order of equal elements is not kept.
func SlicesSortFunc[T any](xs []T, compare func(a T, b T) int) []T {
	out := slices.Clone(xs)
	slices.SortFunc(out, compare)
	return out
}

// SlicesSortStableFunc is slices.SortStableFunc(xs, compare): a copy of xs
// sorted by compare, equal elements in their order in xs.
func SlicesSortStableFunc[T any](xs []T, compare func(a T, b T) int) []T {
	out := slices.Clone(xs)
	slices.SortStableFunc(out, compare)
	return out
}

// SlicesContains is slices.Contains(xs, x).
//
//ergo:pure
func SlicesContains[T comparable](xs []T, x T) bool { return slices.Contains(xs, x) }

// SlicesIndex is slices.Index(xs, x): the index of the first x in xs, or
// -1.
//
//ergo:pure
func SlicesIndex[T comparable](xs []T, x T) int { return slices.Index(xs, x) }

// SlicesReverse is a copy of xs in the reverse order.
//
//ergo:pure
func SlicesReverse[T any](xs []T) []T {
	out := slices.Clone(xs)
	slices.Reverse(out)
	return out
}

// SlicesClone is slices.Clone(xs): a copy of xs in a new array, so a write
// to it does not reach xs. slices.Items returns it, so a caller cannot
// unsort an Ascending (SS5).
//
//ergo:pure
func SlicesClone[T any](xs []T) []T { return slices.Clone(xs) }

// SlicesConcat is slices.Concat(a, b): a new array of at least len(a) +
// len(b), so the result shares no array with a or b. Where a is consumed
// (no binding reads it after the call), the lowering calls its owned form
// SlicesConcatOwned, which appends b to a's array (OWN1).
//
//ergo:pure
//ergo:owned SlicesConcatOwned
func SlicesConcat[T any](a []T, b []T) []T { return slices.Concat(a, b) }

// SlicesConcatOwned is append(a, b...): SlicesConcat's owned form, called
// on a consumed a, whose array nothing else reads, so b's elements go into
// a's spare capacity and a loop of p = slices.Concat(p, q) is linear, as
// hand Go's p = append(p, q...) is. It writes past a's length, so it is
// not marked pure. A test build calls its checking form,
// SlicesConcatPoison, instead.
//
//ergo:check SlicesConcatPoison
func SlicesConcatOwned[T any](a []T, b []T) []T { return append(a, b...) }

// SlicesConcatPoison is SlicesConcatOwned's checking form, which a test
// build calls where the run build calls the owned form (lower/handover.go):
// it makes the new array SlicesConcat makes, then clears a's whole array,
// which the proof says no binding reads again. A read the proof missed
// reads zero values, so the program's own tests see it.
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

// SlicesRepeat is slices.Repeat(xs, n): xs n times over, in a new array.
// A negative n, or a length past int, panics, as Go's does: a defect.
//
//ergo:pure
func SlicesRepeat[T any](xs []T, n int) []T { return slices.Repeat(xs, n) }

// SlicesSlice is xs[i:j:j], the full slice expression: no room after j,
// so an append to the result copies and never writes xs[j]. A bound out
// of range panics: a defect, as an index out of range is.
//
//ergo:pure
func SlicesSlice[T any](xs []T, i int, j int) []T { return xs[i:j:j] }

// MapsClone is maps.Clone(m): a new map with the keys and values of m.
// It is restated, as CmpCompare is, because maps is not in PureImports.
//
//ergo:pure
func MapsClone[K comparable, V any](m map[K]V) map[K]V {
	out := make(map[K]V, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// SlicesMax is slices.Max(xs). An empty xs is a defect, Go's panic; the
// Ergo body of slices.Max calls it only on a non-empty slice.
//
//ergo:pure
func SlicesMax[T ordered](xs []T) T { return slices.Max(xs) }

// SlicesMin is slices.Min(xs), under the rule of SlicesMax.
//
//ergo:pure
func SlicesMin[T ordered](xs []T) T { return slices.Min(xs) }

// SlicesBinarySearch is the index of x in the sorted xs that Go's
// slices.BinarySearch finds, or -1 when xs has no x.
//
//ergo:pure
func SlicesBinarySearch[T ordered](xs []T, x T) int {
	i, found := slices.BinarySearch(xs, x)
	if !found {
		return -1
	}
	return i
}

// SlicesEqual is slices.Equal(a, b): the same length and equal elements in
// order.
//
//ergo:pure
func SlicesEqual[T comparable](a []T, b []T) bool { return slices.Equal(a, b) }

// CmpCompare is Go's cmp.Compare(a, b), restated: -1, 0 or +1. A NaN is
// less than any other float and equal to a NaN, as in Go.
//
//ergo:pure
func CmpCompare[T ordered](a T, b T) int {
	aNaN := a != a
	bNaN := b != b
	if aNaN {
		if bNaN {
			return 0
		}
		return -1
	}
	if bNaN {
		return +1
	}
	if a < b {
		return -1
	}
	if a > b {
		return +1
	}
	return 0
}

// CmpLess is Go's cmp.Less(a, b), restated: a before b in CmpCompare's
// order.
//
//ergo:pure
func CmpLess[T ordered](a T, b T) bool {
	return (a != a && b == b) || a < b
}

// CmpMax is Go's builtin max(a, b): the larger value; for floats a NaN
// gives NaN, and max(-0, +0) is +0.
//
//ergo:pure
func CmpMax[T ordered](a T, b T) T { return max(a, b) }

// CmpMin is Go's builtin min(a, b): the smaller value; for floats a NaN
// gives NaN, and min(-0, +0) is -0.
//
//ergo:pure
func CmpMin[T ordered](a T, b T) T { return min(a, b) }
