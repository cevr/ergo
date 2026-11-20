package snapshot

import (
	"bytes"
	"slices"

	"ergo.local/prototype/internal/sketch/reflectread"
)

// The order of a map's entries in a snapshot (compile-speed CS1b). A Go
// map ranges in a random order, so the generated map codecs (codec_gen.go)
// write the entries in the order entryOrder gives. Ids are given in the
// order the encoder meets objects, so a stable entry order makes every
// later id, and every later byte, stable too.
//
// Law ExportIsDeterministic: two checks of the same std export the same
// bytes. It rests on three facts:
//
//  1. The check is a function of the source (D16): a pass that ranges over
//     a map keyed by nodes and builds identities visits the keys in source
//     order (sourceorder.go).
//  2. The snapshot carries no memo keyed by object identity (exportgen
//     skipFields: the type table's memos), whose keys only identity tells
//     apart.
//  3. A map's entries are written in rank order. A rank names a key, then
//     its value, by what is already stable when the map is written: an
//     external's index, the id of an object written before, the id of a
//     syntax node (encodeSnapshot reaches each package's files first, so
//     every node of a file has its id), else the object's type and fields,
//     rankDepth pointers down.
//
// Keys whose ranks tie (at std, nodes the checker made from one origin)
// stay in the map's order, so they must encode alike:
// TestExportIsDeterministic exports with each run of tied keys reversed
// and gets the same bytes.

// rankDepth is how many pointers a rank follows to an object that has no
// id yet.
const rankDepth = 2

// reverseTiesForTest makes entryOrder reverse each run of keys whose ranks
// tie (TestExportIsDeterministic).
var reverseTiesForTest bool

// entryOrder returns the indexes 0 to n-1 of a map's entries in rank order;
// entry(i) returns pointers to key i and its value.
func (e *encoder) entryOrder(n int, entry func(i int) (key, value any)) []int {
	order := make([]int, n)
	for i := range order {
		order[i] = i
	}
	if n < 2 {
		return order
	}
	ranks := make([][]byte, n)
	for i := range ranks {
		k, v := entry(i)
		ranks[i] = reflectread.Rank(reflectread.Rank(nil, k, rankDepth, e.known), v, rankDepth, e.known)
	}
	slices.SortFunc(order, func(a, b int) int { return bytes.Compare(ranks[a], ranks[b]) })
	if reverseTiesForTest {
		for lo := 0; lo < n; {
			hi := lo + 1
			for hi < n && bytes.Equal(ranks[order[lo]], ranks[order[hi]]) {
				hi++
			}
			slices.Reverse(order[lo:hi])
			lo = hi
		}
	}
	return order
}

// known is the tag and id of a pointer the encoder already knows: an
// external's index (1), the id of an object written before (2), the id of
// a syntax node (3).
func (e *encoder) known(x any) (byte, uint64, bool) {
	if i, ok := e.ext[x]; ok {
		return 1, i, true
	}
	if id, ok := e.ids[x]; ok {
		return 2, id, true
	}
	if id, ok := e.nodes.Has(x); ok {
		return 3, id, true
	}
	return 0, 0, false
}
