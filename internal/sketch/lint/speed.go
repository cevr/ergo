package lint

import (
	"strings"
	"unicode"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// The readers of the speed preset (LH3): the variable a collection is
// read from, whether code writes a variable, the loops of a body and the
// bytes a value of a type takes.

// rootVar is the variable e reads, through field selections: v for v,
// v.items and v.a.b; nil for anything else (a call, an index, a literal).
func rootVar(info *check.Info, e syntax.Expr) *check.Var {
	for {
		switch x := e.(type) {
		case *syntax.ParenExpr:
			e = x.X
		case *syntax.SelectorExpr:
			e = x.X
		case *syntax.Ident:
			v, _ := info.Uses[x].(*check.Var)
			return v
		default:
			return nil
		}
	}
}

// writtenRoot is the variable an assignment to e writes: rootVar through
// field selections and indexes (m[k] = x writes m).
func writtenRoot(info *check.Info, e syntax.Expr) *check.Var {
	for {
		switch x := e.(type) {
		case *syntax.IndexExpr:
			e = x.X
		case *syntax.ParenExpr:
			e = x.X
		case *syntax.SelectorExpr:
			e = x.X
		default:
			return rootVar(info, e)
		}
	}
}

// writes reports whether n assigns v, a field or an element of it, or
// takes its address.
func writes(info *check.Info, n syntax.Node, v *check.Var) bool {
	found := false
	syntax.Inspect(n, func(m syntax.Node) bool {
		switch m := m.(type) {
		case *syntax.AssignStmt:
			for _, l := range m.Lhs {
				if writtenRoot(info, l) == v {
					found = true
				}
			}
		case *syntax.UnaryExpr:
			if m.Op == syntax.AND && rootVar(info, m.X) == v {
				found = true
			}
		}
		return !found
	})
	return found
}

// leavesEarly reports whether a loop body can leave its loop or its
// function before the last turn: a break of this loop, a return or a try
// (function literals and inner loops' breaks aside).
func leavesEarly(body *syntax.BlockStmt) bool {
	leaves := false
	var visit func(n syntax.Node, nested bool) bool
	visit = func(n syntax.Node, nested bool) bool {
		switch n := n.(type) {
		case *syntax.FuncLit:
			return false
		case *syntax.ForStmt, *syntax.RangeStmt:
			syntax.Inspect(n, func(m syntax.Node) bool {
				if m == n {
					return true
				}
				return visit(m, true)
			})
			return false
		case *syntax.BranchStmt:
			if !nested && n.Tok == syntax.BREAK {
				leaves = true
			}
		case *syntax.ReturnStmt, *syntax.TryExpr:
			leaves = true
		}
		return !leaves
	}
	syntax.Inspect(body, func(n syntax.Node) bool { return visit(n, false) })
	return leaves
}

// written is the variables n assigns, at their root.
func written(info *check.Info, n syntax.Node) map[*check.Var]bool {
	out := map[*check.Var]bool{}
	syntax.Inspect(n, func(m syntax.Node) bool {
		if a, ok := m.(*syntax.AssignStmt); ok {
			for _, l := range a.Lhs {
				if v := writtenRoot(info, l); v != nil {
					out[v] = true
				}
			}
		}
		return true
	})
	return out
}

// pointers is the pointer variables n uses.
func pointers(info *check.Info, n syntax.Node) map[*check.Var]bool {
	out := map[*check.Var]bool{}
	syntax.Inspect(n, func(m syntax.Node) bool {
		if id, ok := m.(*syntax.Ident); ok {
			if v, isVar := info.Uses[id].(*check.Var); isVar {
				if _, isPtr := underOf(v.Type).(*check.Pointer); isPtr {
					out[v] = true
				}
			}
		}
		return true
	})
	return out
}

// calls reports whether n holds a call.
func calls(n syntax.Node) bool {
	found := false
	syntax.Inspect(n, func(m syntax.Node) bool {
		if _, ok := m.(*syntax.CallExpr); ok {
			found = true
		}
		return !found
	})
	return found
}

// sumHolds reports whether v is one of the operands of the sum e (a + b
// + c, parentheses read through).
func sumHolds(info *check.Info, e syntax.Expr, v *check.Var) bool {
	switch x := e.(type) {
	case *syntax.ParenExpr:
		return sumHolds(info, x.X, v)
	case *syntax.BinaryExpr:
		return x.Op == syntax.ADD && (sumHolds(info, x.X, v) || sumHolds(info, x.Y, v))
	case *syntax.Ident:
		return info.Uses[x] == v
	}
	return false
}

// usesAny reports whether n reads or assigns one of vars.
func usesAny(info *check.Info, n syntax.Node, vars map[*check.Var]bool) bool {
	if len(vars) == 0 {
		return false
	}
	found := false
	syntax.Inspect(n, func(m syntax.Node) bool {
		if id, ok := m.(*syntax.Ident); ok {
			if v, isVar := info.Uses[id].(*check.Var); isVar && vars[v] {
				found = true
			}
		}
		return !found
	})
	return found
}

// docScans reports whether fn's doc comment has a Scans: paragraph that
// names its i-th parameter first.
func docScans(fn *check.Func, i int) bool {
	doc, _ := check.DocOf(fn)
	text, ok := doc.Scans()
	if !ok || fn.Sig == nil || i >= len(fn.Sig.Params) {
		return false
	}
	// The paragraph starts with the word and its colon; the name follows.
	if colon := strings.IndexByte(text, ':'); colon >= 0 {
		text = strings.TrimSpace(text[colon+1:])
	}
	end := 0
	for end < len(text) && (unicode.IsLetter(rune(text[end])) || unicode.IsDigit(rune(text[end])) || text[end] == '_') {
		end++
	}
	return end > 0 && text[:end] == fn.Sig.Params[i].Name
}

// loopsOver reports whether body, outside function literals, has a for-in
// loop over the parameter v. The body reads its parameters as variables of
// their own declared at the same place, so v is matched by position.
func loopsOver(info *check.Info, body *syntax.BlockStmt, v *check.Var) bool {
	found := false
	syntax.Inspect(body, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.FuncLit:
			return false
		case *syntax.RangeStmt:
			if x, ok := n.X.(*syntax.Ident); ok {
				if u, isVar := info.Uses[x].(*check.Var); isVar && u.Pos == v.Pos {
					found = true
				}
			}
		}
		return !found
	})
	return found
}

// inLoops calls visit for each node of f's function bodies with the
// innermost loop (for or for-in) whose body holds it, or nil. A function
// literal starts again with no loop: its body runs when it is called.
func inLoops(f *syntax.File, visit func(n syntax.Node, loop syntax.Stmt)) {
	var walk func(n syntax.Node, loop syntax.Stmt)
	walk = func(n syntax.Node, loop syntax.Stmt) {
		syntax.Inspect(n, func(m syntax.Node) bool {
			if m == n {
				return true
			}
			switch m := m.(type) {
			case *syntax.FuncLit:
				if m.Body != nil {
					walk(m.Body, nil)
				}
				return false
			case *syntax.RangeStmt:
				visit(m, loop)
				walk(m.X, loop)
				walk(m.Body, m)
				return false
			case *syntax.ForStmt:
				visit(m, loop)
				if m.Cond != nil {
					walk(m.Cond, m)
				}
				walk(m.Body, m)
				return false
			}
			visit(m, loop)
			return true
		})
	}
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *syntax.FuncDecl:
			if d.Body != nil {
				walk(d.Body, nil)
			}
		case *syntax.TestDecl:
			walk(d.Body, nil)
		case *syntax.LawDecl:
			if d.Body != nil {
				walk(d.Body, nil)
			}
		}
	}
}

// underOf is t's underlying type: a declared type's, or t.
func underOf(t check.Type) check.Type {
	if n, ok := t.(*check.Named); ok {
		return n.Underlying()
	}
	return t
}

// valueBytes is the bytes a value of t takes in the lowered Go, without
// padding (so at least this many): a string and a union or interface 16, a
// slice 24, a map, pointer or function 8, a record its fields, an array
// its elements.
func valueBytes(t check.Type) int64 {
	return sizeOf(t, 0)
}

func sizeOf(t check.Type, depth int) int64 {
	if depth > 16 {
		return 8
	}
	switch t := underOf(t).(type) {
	case *check.Basic:
		switch t.Kind {
		case check.Bool, check.Int8, check.Uint8:
			return 1
		case check.Int16, check.Uint16:
			return 2
		case check.Int32, check.Uint32, check.Float32:
			return 4
		case check.String, check.Complex128:
			return 16
		}
		return 8
	case *check.Struct:
		var n int64
		for _, f := range t.Fields {
			n += sizeOf(f.Type, depth+1)
		}
		return n
	case *check.Array:
		return t.Len * sizeOf(t.Elem, depth+1)
	case *check.Tuple:
		var n int64
		for _, e := range t.Types {
			n += sizeOf(e, depth+1)
		}
		return n
	case *check.Slice:
		return 24
	case *check.Interface, *check.Union:
		return 16
	}
	return 8
}
