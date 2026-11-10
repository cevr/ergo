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
