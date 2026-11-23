package lower

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Seeded capacity (MAPCOPY RU11). A let mut that a body starts from a
// one-element slice literal and then grows in place
//
//	cur = []pt{p}
//	…
//	cur = cur.Append(q)
//
// cost Go two allocations for its first two elements: the literal's array
// of one, then append's array of two. hosts/mygo's flatten starts each
// contour so, and most of its contours hold two points: 226 of its 569
// allocations a frame were the literals and 259 the first growth.
//
// The IR lowers such a literal as append(slices.Grow([]T{}, seedCap), e):
// one allocation of room for seedCap elements, and the next appends find
// room. The value is the literal's (one element, its length one); only its
// capacity differs, as a slice an in-place Append grew has room past its
// length, which the value rule already allows for (an Append that may not
// write in place clips its source).
//
// The literal seeds when it has one element, it is the whole value of
// `let mut x = …` or `x = …` for a local let mut x, and the body grows x
// in place: some statement is `x = x.Append(e)`. seedCap is Rust's
// RawVec::MIN_NON_ZERO_CAP for an element of at most 1 KiB, the room its
// first growth makes.
//
// The reference is seedLowering "exact": the literal as written, an array
// of its length. TestValueRuleAgrees runs its programs under the rule and
// the copying reference, which takes the exact form; contours' promised
// form is checked under both.
//
// Prior art: Rust's Vec grows from zero to four elements at its first push;
// Go's append grows a one-element array to two; Swift's Array reserves by
// its allocator's size class. Gleam, Elixir and ReScript build lists by
// consing, one cell per element, so no capacity is chosen.
var seedLowering string

// SetSeedCap turns the seeded capacity on (the rule) or off (the
// reference, "exact"), for a test of another package; restore undoes it.
func SetSeedCap(on bool) (restore func()) {
	saved := seedLowering
	seedLowering = "exact"
	if on {
		seedLowering = ""
	}
	return func() { seedLowering = saved }
}

// seedCap is the room a seeded literal makes (above).
const seedCap = 4

// seedLits marks the literals of this body that seed (vf.seeds).
func (w *valueWalk) seedLits() {
	if seedLowering == "exact" || !valueOwned() || w.root.node == nil {
		return
	}
	grown := map[*check.Var]bool{}
	for _, call := range w.calls {
		c := w.info.Calls[call]
		if c == nil || !appendsSource(c.Func) || check.GoBuiltin(c.Func) == "append" && check.AppendsString(c.Func) {
			continue
		}
		parent, child := w.unparenParent(call)
		as, ok := parent.(*syntax.AssignStmt)
		if !ok || as.Tok != syntax.ASSIGN || len(as.Lhs) != 1 || len(as.Rhs) != 1 || syntax.Node(as.Rhs[0]) != child {
			continue
		}
		lhs, ok := unparen(as.Lhs[0]).(*syntax.Ident)
		recv, ok2 := unparen(w.recvOf(call)).(*syntax.Ident)
		if !ok || !ok2 {
			continue
		}
		if v, ok := w.info.Uses[lhs].(*check.Var); ok && w.info.Uses[recv] == v {
			grown[v] = true
		}
	}
	if len(grown) == 0 {
		return
	}
	seed := func(v *check.Var, e syntax.Expr) {
		lit, ok := unparen(e).(*syntax.CompositeLit)
		if !ok || lit.Base != nil || lit.Type == nil || len(lit.Elts) != 1 || !grown[v] {
			return
		}
		if _, spread := w.info.Spreads[lit]; spread {
			return
		}
		if _, kv := lit.Elts[0].(*syntax.KeyValueExpr); kv {
			return
		}
		if _, ok := underEqual(w.typeOf(lit)).(*check.Slice); !ok {
			return
		}
		if vv := w.vars[v]; vv == nil || !vv.mut || !vv.local {
			return
		}
		w.vf.seeds[lit] = true
	}
	syntax.Inspect(w.root.node, func(n syntax.Node) bool {
		switch s := n.(type) {
		case *syntax.AssignStmt:
			if s.Tok == syntax.ASSIGN && len(s.Lhs) == 1 && len(s.Rhs) == 1 {
				if id, ok := unparen(s.Lhs[0]).(*syntax.Ident); ok {
					if v, ok := w.info.Uses[id].(*check.Var); ok {
						seed(v, s.Rhs[0])
					}
				}
			}
		case *syntax.VarSpec:
			if s.Pattern == nil && len(s.Names) == 1 && len(s.Values) == 1 {
				if v, ok := w.info.Defs[s.Names[0]].(*check.Var); ok {
					seed(v, s.Values[0])
				}
			}
		}
		return true
	})
}
