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
	CollNone   CollOp = iota
	CollAppend        // xs.Append(x): xs with x added at the end
	CollGet           // m.Get(k): Some of k's value, or None
	CollPut           // m.Put(k, v): m with k set to v
	CollDelete        // m.Delete(k): m without k
	CollTake          // m.Take(k): k's value as an Option, and m without k
)

// collOps are the operations by the type they are on (D-D8: reached only
// by the dot form on exactly an unnamed slice or Map) and their name.
var collOps = map[collKey]CollOp{
	{name: "Append"}:              CollAppend,
	{name: "Get", isMap: true}:    CollGet,
	{name: "Put", isMap: true}:    CollPut,
	{name: "Delete", isMap: true}: CollDelete,
	{name: "Take", isMap: true}:   CollTake,
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
