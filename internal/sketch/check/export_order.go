package check

import (
	"bytes"
	"encoding/binary"
	"math"
	"reflect"
	"slices"
)

// The order of a map's entries in a snapshot (compile-speed CS1b). A Go
// map ranges in a random order, so the generated map codecs (export_gen.go)
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
		ranks[i] = e.rank(e.rank(nil, reflect.ValueOf(k).Elem(), rankDepth), reflect.ValueOf(v).Elem(), rankDepth)
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

// rank appends v's rank to b. depth is how many pointers below v a rank
// may follow to an object that has no id yet.
func (e *encoder) rank(b []byte, v reflect.Value, depth int) []byte {
	switch v.Kind() {
	case reflect.Invalid:
		return append(b, 0)
	case reflect.Interface:
		if v.IsNil() {
			return append(b, 0)
		}
		return e.rank(b, v.Elem(), depth)
	case reflect.Pointer:
		if v.IsNil() {
			return append(b, 0)
		}
		// A pointer read from an unexported field cannot be an interface
		// value; one made at the same address can.
		x := reflect.NewAt(v.Type().Elem(), v.UnsafePointer()).Interface()
		if i, ok := e.ext[x]; ok {
			return binary.BigEndian.AppendUint64(append(b, 1), i)
		}
		if id, ok := e.ids[x]; ok {
			return binary.BigEndian.AppendUint64(append(b, 2), id)
		}
		if id, ok := e.nodes.Has(x); ok {
			return binary.BigEndian.AppendUint64(append(b, 3), id)
		}
		b = rankString(append(b, 4), v.Type().String())
		if depth == 0 {
			return b
		}
		return e.rank(b, v.Elem(), depth-1)
	case reflect.Struct:
		b = append(b, 5)
		for i := range v.NumField() {
			b = e.rank(b, v.Field(i), depth)
		}
		return b
	case reflect.Slice:
		if v.IsNil() {
			return append(b, 0)
		}
		b = binary.BigEndian.AppendUint64(append(b, 6), uint64(v.Len()))
		for i := range v.Len() {
			b = e.rank(b, v.Index(i), depth)
		}
		return b
	case reflect.Array:
		b = append(b, 6)
		for i := range v.Len() {
			b = e.rank(b, v.Index(i), depth)
		}
		return b
	case reflect.String:
		return rankString(append(b, 7), v.String())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return binary.BigEndian.AppendUint64(append(b, 8), uint64(v.Int())^(1<<63))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return binary.BigEndian.AppendUint64(append(b, 8), v.Uint())
	case reflect.Bool:
		if v.Bool() {
			return append(b, 8, 1)
		}
		return append(b, 8, 0)
	case reflect.Float32, reflect.Float64:
		return binary.BigEndian.AppendUint64(append(b, 8), math.Float64bits(v.Float()))
	case reflect.Map:
		// A map has no order to rank its entries by: its length alone.
		return binary.BigEndian.AppendUint64(append(b, 9), uint64(v.Len()))
	default:
		// A function or a channel.
		return append(b, 9)
	}
}

func rankString(b []byte, s string) []byte {
	b = binary.BigEndian.AppendUint64(b, uint64(len(s)))
	return append(b, s...)
}
