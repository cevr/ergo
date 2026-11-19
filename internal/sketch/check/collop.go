package check

import (
	"strings"

	"ergo.local/prototype/internal/sketch/syntax"
)

// The operations of the built-in slice and Map types (PRIM, owner
// 2026-11-16): xs.Append(x), m.Get(k), m.Put(k, v), m.Delete(k) and
// m.Take(k). They belong to the language, the same in every program, as
// + belongs to int: the evaluator, the footprint, the value rule and both
// lowerings read which operation a call is from CollOpOf, the one place
// that names them, and never from a package path or a function's name.
// The value rule's in-place-when-dead choice stays the general ownership
// rule (lower values.go), never keyed on the operation.

// collDecls declare the operations (PRIM-2b): the language's own source,
// which the checker adds to the prelude package as the file collFileName,
// so Option is in scope and every package reaches the operations by the
// dot form. No std file declares them, and the registration reads this
// file, never a package path. ref is the Go reference each one's body
// calls, runtime/v1's (Go's delete for Delete): the checker binds it here,
// in Go, so no Ergo source binds the operations to Go; the laws beside
// them in the prelude are checked against these bodies (runtime/v1
// TestCollLaws). The doc comments are the operations' documentation.
var collDecls = []struct{ doc, ref, sig string }{
	{`// Append is xs with x added at the end. xs is unchanged: a slice is a
// value (owner q26; types.md, Bindings), and the lowering appends in place
// only for xs = xs.Append(x) or a dead xs. With resource elements it takes
// xs and x: the result is their one owner (#197 L8 A3, resources R14).`,
		"ergo.local/prototype/runtime/v1.Append", "func Append[T](xs own []T, x own T) []T"},
	{`// Get is the value of k in m, Some{v}, or None when m has no key k: a Map
// read may miss, so it returns an Option.`,
		"ergo.local/prototype/runtime/v1.Get", "func Get[K comparable, V](m Map[K, V], k K) Option[V]"},
	{`// Delete is m without the key k. m is unchanged: a Map is a value (owner
// q26), and the lowering deletes in place only for m = m.Delete(k) or a
// dead m. Its body is Go's builtin delete (OPT7): the call is the builtin
// itself, with no Go function between.`,
		"delete", "func Delete[K comparable, V](m Map[K, V], k K) Map[K, V]"},
	{`// Put is m with k set to v: the entry is added, or its value replaced. m
// is unchanged, as Delete leaves it (owner q26), and the lowering writes
// in place only for m = m.Put(k, v) or a dead m (#196 item 13, L2).`,
		"ergo.local/prototype/runtime/v1.Put", "func Put[K comparable, V](m Map[K, V], k K, v V) Map[K, V]"},
	{`// Take gives up m and returns the value at k (Some, or None if absent)
// and the map without k. Resource values and the remainder have separate
// owners. With plain-data values, m is unchanged (mode crossing), and the
// lowering takes in place only for a dead m (PRIM-3).`,
		"ergo.local/prototype/runtime/v1.MapTake", "func Take[K comparable, V](m own Map[K, V], k K) (Option[V], Map[K, V])"},
	{`// TakeAt gives up xs and returns its element at i and the remaining
// slice, in order. With resource elements, the element and remainder have
// separate owners. Plain-data xs is unchanged. An index outside
// 0..len(xs) is a defect, as Go's index is (PRIM-4).`,
		"ergo.local/prototype/runtime/v1.SlicesTake", "func TakeAt[T](xs own []T, i int) (T, []T)"},
	{`// Slice is the elements of xs from index i up to j, j excluded: Go's full
// slice expression xs[i:j:j] (Effect's Array.slice), which shares xs's
// array but has no room after j, so an Append to it makes a new array and
// never writes xs[j]. A bound outside 0..len(xs), or an i after j, is a
// defect (PRIM-4).`,
		"ergo.local/prototype/runtime/v1.SlicesSlice", "func Slice[T](xs []T, i int, j int) []T"},
	{`// Clone is a Map with the keys and values of m: Go's maps.Clone. A
// write to the clone never reaches m (PRIM-4).`,
		"ergo.local/prototype/runtime/v1.MapsClone", "func Clone[K comparable, V](m Map[K, V]) Map[K, V]"},
	{`// Clone is a copy of xs in a new array: Go's slices.Clone (PRIM-4).`,
		"ergo.local/prototype/runtime/v1.SlicesClone", "func Clone[T](xs []T) []T"},
	{`// Concat is xs followed by ys, in a new array: Go's slices.Concat. Where
// xs is consumed, the lowering appends ys to xs's array (OWN1; PRIM-4).`,
		"ergo.local/prototype/runtime/v1.SlicesConcat", "func Concat[T](xs []T, ys []T) []T"},
	{`// Repeat is xs n times over, in a new array: Go's slices.Repeat. A
// negative n is a defect (PRIM-4).`,
		"ergo.local/prototype/runtime/v1.SlicesRepeat", "func Repeat[T](xs []T, n int) []T"},
	{`// Slice is the text of s from byte i up to byte j, j excluded: Go's
// s[i:j]. A bound outside 0..len(s), or an i after j, is a defect
// (PRIM-4).`,
		"ergo.local/prototype/runtime/v1.StringsSlice", "func Slice(s string, i int, j int) string"},
	{`// Bytes is the bytes of s, a copy: Go's []byte(s) (PRIM-4).`,
		"ergo.local/prototype/runtime/v1.StringsBytes", "func Bytes(s string) []byte"},
	{`// AppendString is bs with the bytes of s added: Go's append(bs, s...)
// (PRIM-4).`,
		"ergo.local/prototype/runtime/v1.StringsBuilderAppend", "func AppendString(bs []byte, s string) []byte"},
	{`// AppendBytes is bs with the bytes of more added: Go's append(bs,
// more...) (PRIM-4).`,
		"ergo.local/prototype/runtime/v1.StringsBuilderAppendBytes", "func AppendBytes(bs []byte, more []byte) []byte"},
	{`// AppendFrom is bs with n bytes added, copied from bs at start on, as an
// LZ77 decoder copies a match: byte k of the n is the byte at start+k of
// the result. A negative n, or a start outside bs, is a defect (PRIM-4).`,
		"ergo.local/prototype/runtime/v1.StringsBuilderAppendFrom", "func AppendFrom(bs []byte, start int, n int) []byte"},
	{`// Grow is bs with room for n more bytes. A negative n is a defect
// (PRIM-4).`,
		"ergo.local/prototype/runtime/v1.StringsBuilderGrow", "func Grow(bs []byte, n int) []byte"},
	{`// Text is the text of bs's bytes: Go's string(bs). Where bs is
// consumed, the text shares its bytes with no copy (OWN1; PRIM-4).`,
		"ergo.local/prototype/runtime/v1.StringsBuilderText", "func Text(bs []byte) string"},
	{`// RuneText is the UTF-8 text of r, or "\uFFFD" for a rune that is not
// valid: Go's string(r) (PRIM-4).`,
		"ergo.local/prototype/runtime/v1.UTF8RuneText", "func RuneText(r rune) string"},
	{`// MakeSlice is an empty slice with room for capacity elements: Go's
// make([]T, 0, capacity). It makes no element; a negative capacity is a
// defect (PRIM-4).`,
		"ergo.local/prototype/runtime/v1.WithCapacity", "func MakeSlice[T](capacity int) []T"},
}

// collFileName is the name of the operations' file in the prelude package.
const collFileName = "builtin.ergo"

// collFile is the operations' file, parsed once with the prelude: the
// checker registers the functions of this file, and of no other, as the
// operations (decl.go).
var collFile *syntax.File

// collSource is the text of the operations' file.
func collSource() string {
	var b strings.Builder
	b.WriteString("// The operations of the built-in slice and Map types (check/collop.go).\n")
	for _, d := range collDecls {
		b.WriteString("\n" + d.doc + "\nextern go " + d.ref + " " + d.sig + "\n")
	}
	return b.String()
}

// CollOp is an operation of the built-in slice or Map type, or CollNone.
type CollOp uint8

const (
	CollNone         CollOp = iota
	CollAppend              // xs.Append(x): xs with x added at the end
	CollGet                 // m.Get(k): Some of k's value, or None
	CollPut                 // m.Put(k, v): m with k set to v
	CollDelete              // m.Delete(k): m without k
	CollTake                // m.Take(k): k's value as an Option, and m without k
	CollTakeAt              // xs.TakeAt(i): the element at i, and xs without it
	CollSlice               // xs.Slice(i, j): the elements from i up to j
	CollClone               // m.Clone(): a Map of m's entries (PRIM-4)
	CollSliceClone          // xs.Clone(): a copy of xs
	CollConcat              // xs.Concat(ys): xs then ys
	CollRepeat              // xs.Repeat(n): xs n times over
	CollSubstring           // s.Slice(i, j): the text from byte i up to j
	CollBytes               // s.Bytes(): the bytes of s
	CollAppendString        // bs.AppendString(s): bs with s's bytes added
	CollAppendBytes         // bs.AppendBytes(more): bs with more added
	CollAppendFrom          // bs.AppendFrom(start, n): bs with n bytes copied from start
	CollGrow                // bs.Grow(n): bs with room for n more
	CollText                // bs.Text(): the text of bs
	CollRuneText            // r.RuneText(): the text of r
	CollMakeSlice           // MakeSlice[T](capacity): an empty slice with room
)

// collMakers are the language's functions of its file that no receiver
// reaches, called by name like len (PRIM-4).
var collMakers = map[string]CollOp{"MakeSlice": CollMakeSlice}

// collOn is the kind of receiver an operation is on: exactly an unnamed
// slice []T, a Map, the basic string, an unnamed []byte, or a rune (D-D8).
// A []byte reaches the []byte operations and those of []T.
type collOn uint8

const (
	onSlice collOn = iota
	onMap
	onString
	onBytes
	onRune
)

// collOps are the operations by the type they are on (D-D8: reached only
// by the dot form on exactly an unnamed slice or Map) and their name.
var collOps = map[collKey]CollOp{
	{name: "Append"}:                    CollAppend,
	{name: "Get", on: onMap}:            CollGet,
	{name: "Put", on: onMap}:            CollPut,
	{name: "Delete", on: onMap}:         CollDelete,
	{name: "Take", on: onMap}:           CollTake,
	{name: "TakeAt"}:                    CollTakeAt,
	{name: "Slice"}:                     CollSlice,
	{name: "Clone", on: onMap}:          CollClone,
	{name: "Clone"}:                     CollSliceClone,
	{name: "Concat"}:                    CollConcat,
	{name: "Repeat"}:                    CollRepeat,
	{name: "Slice", on: onString}:       CollSubstring,
	{name: "Bytes", on: onString}:       CollBytes,
	{name: "AppendString", on: onBytes}: CollAppendString,
	{name: "AppendBytes", on: onBytes}:  CollAppendBytes,
	{name: "AppendFrom", on: onBytes}:   CollAppendFrom,
	{name: "Grow", on: onBytes}:         CollGrow,
	{name: "Text", on: onBytes}:         CollText,
	{name: "RuneText", on: onRune}:      CollRuneText,
}

// CollOpOf is the operation of the built-in slice or Map type that f
// declares, or CollNone for every other function.
func CollOpOf(f *Func) CollOp {
	if f == nil {
		return CollNone
	}
	return f.collOp
}

// String is the operation's dot-form name.
func (op CollOp) String() string {
	for k, o := range collOps {
		if o == op {
			return k.name
		}
	}
	return "none"
}
