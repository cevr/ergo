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
// order. A float NaN sorts first, as in Go.
//
//ergo:pure
func SlicesSort[T ordered](xs []T) []T {
	out := slices.Clone(xs)
	slices.Sort(out)
	return out
}

// SlicesSortOwned sorts xs in place and returns it. Only slices.Sorted
// calls it, on the slice it has just collected, which nothing else holds.
// It writes its argument, so it is not marked pure.
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
