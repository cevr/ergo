package lower

import (
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Repeat of a constant length (wf10 OWN1 PA3; examples/mdhtml's
// processEmphasis, `let mut a = slices.Repeat([]int{bottom}, 12)`, 11% of
// mdhtml's allocated objects against the hand control's `var
// openersBottom [12]int`). A call slices.Repeat(lit, n) whose first
// operand is a slice literal of m elements and whose count is an integer
// literal makes its result in the caller, with Go's make of the constant
// length m*n, and fills it with v1.SlicesFill:
//
//	ergo.SlicesFill[int](make([]int, 12), []int{bottom})
//
// Go's escape analysis keeps a make of constant length on the caller's
// stack when the slice does not escape (and moves it to the heap when it
// does, or past its stack limit), so the call allocates nothing where
// the hand control's array does not, and as much as SlicesRepeat
// elsewhere. The value is the same: n copies of the literal's elements in
// a new array, evaluated in the same order (the elements, then the
// count, which is a literal).
//
// The reference is repeatLowering "call", every Repeat a call of
// v1.SlicesRepeat: set by a test, or by a build with
// -ldflags=-X=ergo.local/prototype/internal/sketch/lower.repeatLowering=call.
// TestValueRuleAgrees holds the filled form to a model of Go's
// slices.Repeat on seeded literals and counts, written into after (its
// paths "repeat literal" and "repeat binding"), and TestValueRuleElides
// holds a site to the form.
//
// Prior art: Go itself stack-allocates `make([]T, const)` that does not
// escape; OCaml's flambda and GHC unbox small known-size arrays only
// through their own escape analyses; ReScript lowers Belt.Array.make to
// JavaScript's Array, always on the heap; Gleam and Elixir lists are heap
// cells on the BEAM, which has no stack-allocated aggregate.
var repeatLowering string

// repeatFillLimit is the most elements a filled Repeat makes: past it the
// call stays SlicesRepeat (Go would put the make on the heap anyway).
const repeatFillLimit = 1 << 16

// argSource is the operand x stands for: when x names a parameter of the
// inlined body being lowered, the caller's operand (argOf), else x.
func (fc *funcCtx) argSource(x syntax.Expr) syntax.Expr {
	if id, ok := unparen(x).(*syntax.Ident); ok {
		if v, ok := fc.l.info.Uses[id].(*check.Var); ok {
			if a, ok := fc.argOf[v]; ok {
				return a
			}
		}
	}
	return x
}

// repeatFill reports the Go text of the call x of slices.Repeat with the
// operands args in its filled form, or false.
func (fc *funcCtx) repeatFill(x *syntax.CallExpr, args []syntax.Expr) (string, bool) {
	if repeatLowering == "call" || len(args) != 2 {
		return "", false
	}
	// The operands as the call's writer wrote them: since PRIM-4 std's
	// slices.Repeat is the Ergo body { xs.Repeat(n) }, inlined at its site,
	// so a Repeat of a literal and a constant reaches here as the inlined
	// parameters xs and n. The elements are still read from the operand's
	// lowered value below (the parameter's local), so nothing is evaluated
	// twice; only the length comes from the literal and the count.
	lit, ok := unparen(fc.argSource(args[0])).(*syntax.CompositeLit)
	if !ok || lit.Base != nil || len(lit.Elts) == 0 {
		return "", false
	}
	for _, e := range lit.Elts {
		if _, kv := e.(*syntax.KeyValueExpr); kv {
			return "", false
		}
	}
	b, ok := unparen(fc.argSource(args[1])).(*syntax.BasicLit)
	if !ok || b.Kind != syntax.INT {
		return "", false
	}
	n, err := strconv.ParseInt(strings.ReplaceAll(b.Value, "_", ""), 0, 64)
	if err != nil || n < 0 || n*int64(len(lit.Elts)) > repeatFillLimit {
		return "", false
	}
	s, ok := underEqual(fc.typeOf(x)).(*check.Slice)
	if !ok {
		return "", false
	}
	elem := fc.goType(s.Elem)
	vals := fc.callArgs(args, x)
	if unparen(args[1]) != syntax.Expr(b) {
		// The count is an inlined parameter's local, which the fill does
		// not read: Go refuses a local it never uses.
		fc.line("_ = " + vals[1])
	}
	size := strconv.FormatInt(n*int64(len(lit.Elts)), 10)
	fc.l.stats.RepeatFills++
	return fc.l.rt() + ".SlicesFill[" + elem + "](make([]" + elem + ", " + size + "), " + vals[0] + ")", true
}
