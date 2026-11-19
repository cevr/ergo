package check

import "ergo.local/prototype/internal/sketch/syntax"

// The operations of the built-in slice and Map types (PRIM, owner
// 2026-11-16): xs.Append(x), m.Get(k), m.Put(k, v), m.Delete(k) and
// m.Take(k). They belong to the language, the same in every program, as
// + belongs to int: the evaluator, the footprint, the value rule and both
// lowerings read which operation a call is from CollOpOf, the one place
// that names them, and never from a package path or a function's name.
// The value rule's in-place-when-dead choice stays the general ownership
// rule (lower values.go), never keyed on the operation.
//
// The operations are declared in Ergo, in the prelude package's file
// collFileName (internal/sketch/std/builtin.ergo, PRELUDE-ONE-SOURCE):
// their signatures, documentation and runtime/v1 reference bodies have
// that one source, which this checker and compiler/check both read. This
// file only names which operation each declaration is (collOps,
// collMakers).

// collFileName is the name of the operations' file in the prelude package.
const collFileName = "builtin.ergo"

// collFile is the operations' file, parsed once with the prelude: the
// checker registers the functions of this file, and of no other, as the
// operations (decl.go).
var collFile *syntax.File

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
