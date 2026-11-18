package v1

import (
	"maps"
	"slices"
)

// WireKeys are the keys of a Map in order, the order a derived encode
// writes a Map's entries in (types.md, Codecs; L11): one wire form per
// value, whatever Go's map iteration order is.
func WireKeys[K ~string, V any](m map[K]V) []K {
	return slices.Sorted(maps.Keys(m))
}

// WireKeyRoom is how many keys WireKeysIn sorts in its caller's array.
const WireKeyRoom = 8

// WireKeysIn are WireKeys(m), in buf while m has at most WireKeyRoom keys
// (CW-IR's codec writers). buf is an array of the caller's frame, one per
// Map the codec function writes, and the result is ranged in that frame:
// buf leaks only to the result, so it stays on the stack and a Map of up
// to WireKeyRoom keys allocates nothing. Its keys are sorted by insertion
// as they arrive; a bigger Map's go to the heap and slices.Sort.
func WireKeysIn[K ~string, V any](buf *[WireKeyRoom]K, m map[K]V) []K {
	if len(m) > WireKeyRoom {
		return WireKeys(m)
	}
	ks := buf[:0]
	for k := range m {
		i := len(ks)
		ks = ks[:i+1]
		for i > 0 && ks[i-1] > k {
			ks[i] = ks[i-1]
			i--
		}
		ks[i] = k
	}
	return ks
}
