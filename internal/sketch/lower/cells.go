package lower

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Provider cells (types.md, Interface assignability 6; check/cells.go). A
// value provider's value that converts to its sealed operation type moves
// into a cell: the operations adapter (calls.go, operations) holds the
// value in its field v, and each operation on *P takes &a.v. The adapter
// is one allocation, as Go's &T{…} is.
//
// The cell is a new root that the operations write, as a `let mut` is, so
// the value rule (values.go) copies a value that enters it unless the
// value is fresh or a local dead after the conversion (move on last use):
// a write through the cell is never seen through the source, nor the
// reverse.
//
// A window of operation calls on an immutable local that a let binds by a
// cell conversion runs on the cell's own value (cellWindow), so the
// provider's proved laws rewrite it as they rewrite a window on a P local
// (rewrite.go), with the same evidence.
//
// The reference lowering copies at every cell conversion: cellLowering
// "copy", set by a test or by a build with
// -ldflags=-X=ergo.local/prototype/internal/sketch/lower.cellLowering=copy.
// TestProviderCellsAgree holds both to an interpreter that copies the value
// at the conversion.
//
// Prior art: Swift boxes a value captured by an escaping closure in a
// heap cell and copies a value type into an existential's buffer at the
// conversion; Rust's Box::new(v) moves v into the box; Go's &T{…} escapes
// to the heap. Gleam, Elixir and ReScript have no cell a value moves into.
var cellLowering = ""

// cellSite reports whether x moves into a provider cell: its implicit
// conversion, or one of its elements' (a multi-name let), is a cell's.
func cellSite(info *check.Info, x syntax.Expr) bool {
	if conv, ok := info.Implicit[x]; ok && conv.Cell {
		return true
	}
	for _, conv := range info.ImplicitElems[x] {
		if conv.Cell {
			return true
		}
	}
	return false
}

// cellCopy decides the copy of x, a value that moves into a cell.
func (w *valueWalk) cellCopy(x syntax.Expr) {
	if cellLowering == "copy" {
		if w.l.holdsValue(w.typeOf(x)) {
			w.vf.clone[x] = true
		}
		return
	}
	w.freshen(x)
}

// noteCell records the immutable local v when its let's value x moves
// into a cell of a provider of no type arguments.
func (fc *funcCtx) noteCell(v *check.Var, x syntax.Expr) {
	conv, ok := fc.l.info.Implicit[x]
	if !ok || !conv.Cell {
		return
	}
	if from, ok := conv.From.(*check.Named); !ok || len(from.TArgs) > 0 {
		return
	}
	if fc.cells == nil {
		fc.cells = map[*check.Var]check.Conversion{}
	}
	fc.cells[v] = conv
}

// cellWindow is the cell conversion that bound the local v and the Go
// text of the cell's value, when a let in this function binds v,
// immutable, by one (noteCell); ok is false otherwise.
func (fc *funcCtx) cellWindow(v *check.Var) (conv check.Conversion, recv string, ok bool) {
	c, ok := fc.cells[v]
	if !ok {
		return check.Conversion{}, "", false
	}
	key, _, ok := fc.l.adapterName(c)
	if !ok {
		return check.Conversion{}, "", false
	}
	name, ok := fc.l.adapters[key]
	if !ok {
		return check.Conversion{}, "", false
	}
	return c, "&" + fc.locals[v] + ".(*" + name + ").v", true
}
