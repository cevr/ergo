package lower

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// A bound by use for flat unions (R3b; union-repr.md, Threshold and
// round 2; types.md Unions 6). The register bound of flatunion.go's step 4
// (32 bytes, 4 fields) was measured for a value carried unchanged through
// calls: above it Go passes the struct through memory, 15 to 19 ns per
// call more than a box's two words. A value built and consumed at once
// (built, stored in a slice or a record, read back and matched) gains at
// every size measured, because the box costs an allocation per value and
// a load per read. So a union past the register bound stays flat when no
// value of it is carried: when no call passes one to a parameter that
// carries it on.
//
// A parameter carries its argument (carries) when the callee returns it
// unchanged (a return or a tail), passes it unchanged to a parameter that
// carries it, binds it whole to another name, captures it in a function
// literal, or hands it to a callee the lowering cannot see (a function
// value, an operation, a conversion). A Go function (an extern) is a leaf:
// it reads or stores its argument, one copy, as a store into a slice is.
// Every other use consumes it: a match, a field read, a store into a
// record, a slice or a Map, a comparison. A recursive function carries
// every parameter while it is decided.
//
// A union is carried (carriedUnions) when some call of the program passes
// a value whose type is the union to a carrying parameter, of an Ergo body
// (a generic one too: a parameter of type T carries a union T stands for)
// or of a callee the lowering cannot see.
//
// The flat form keeps one more bound, on memory, not registers: a slice
// of a flat union holds every element at the size of its widest form, so
// a union of more than useBytes bytes stays an interface (each box is the
// size of its own variant).
//
// The reference is unionBound "register": the register bound alone, as
// before R3b. TestFlatUnionsAgree runs generated unions under the rule and
// both references, and checks each union's form against the model,
// carried or not. knownStats.UsedFlat counts the unions R3b keeps flat.
//
// Prior art: Rust and Swift lay every enum out inline at any size and
// leave the box (Box, indirect) to the programmer; Rust's
// clippy::large_enum_variant flags a variant much larger than the others,
// the waste this file bounds. OCaml and ReScript box every constructor
// with fields; Gleam and Elixir keep a variant a tagged tuple on the
// BEAM's heap. None of them decides the form from the uses.
var unionBound string

// useBytes is the memory bound of a union flat by use (above).
const useBytes = 128

// flatByUse reports whether the union tn may be flat past the register
// bound: no call carries a value of it, and the reference is not chosen.
func (l *lowerer) flatByUse(tn *check.TypeName) bool {
	return unionBound != "register" && !l.carriedUnions()[tn]
}

// carriedUnions is the set of the program's unions some call carries
// (above), computed once.
func (l *lowerer) carriedUnions() map[*check.TypeName]bool {
	if l.carried != nil {
		return l.carried
	}
	l.carried = map[*check.TypeName]bool{}
	for _, p := range l.sourcedPkgs() {
		info := p.Info
		for _, f := range p.Files() {
			syntax.Inspect(f, func(n syntax.Node) bool {
				x, ok := n.(*syntax.CallExpr)
				if !ok {
					return true
				}
				c := info.Calls[x]
				if c == nil {
					return true
				}
				for j, a := range callOperands(x, c) {
					t, typed := typedIn(info, a)
					if !typed {
						continue
					}
					u, ok := t.(*check.Named)
					if !ok || l.carried[u.Obj] {
						continue
					}
					if _, union := u.Underlying().(*check.Union); !union {
						continue
					}
					if l.passCarries(c, j) {
						l.carried[u.Obj] = true
					}
				}
				return true
			})
		}
	}
	return l.carried
}

// passCarries reports whether operand j of the call c is carried on by its
// callee (above).
func (l *lowerer) passCarries(c *check.Call, j int) bool {
	switch c.Kind {
	case check.CallFunc:
	case check.CallLen:
		return false
	default:
		return true // an operation, a function value, a conversion
	}
	g := c.Func
	if g == nil {
		return true
	}
	if g.Decl != nil && g.Decl.Extern != nil {
		return false // a Go leaf
	}
	if g.Decl == nil || g.Decl.Body == nil {
		return true
	}
	k := l.carries(g)
	return j >= len(k) || k[j]
}

// carries is f's summary, indexed as f.Sig.Params: whether parameter i
// carries its argument on (above).
func (l *lowerer) carries(f *check.Func) []bool {
	all := func() []bool {
		out := make([]bool, len(f.Sig.Params))
		for i := range out {
			out[i] = true
		}
		return out
	}
	d := f.Decl
	if d == nil || d.Body == nil || d.Extern != nil || f.Pkg == nil || f.Pkg.Info == nil {
		return all()
	}
	if l.carrying == nil {
		l.carrying = map[*check.Func][]bool{}
	}
	if k, ok := l.carrying[f]; ok {
		if k == nil {
			return all() // being decided: recursion carries everything
		}
		return k
	}
	vars := paramVars(f)
	if len(vars) != len(f.Sig.Params) {
		return all()
	}
	l.carrying[f] = nil
	info := f.Pkg.Info
	fl := newFlow(l, info, d)
	out := make([]bool, len(vars))
	for i, v := range vars {
		if v == nil {
			continue
		}
		for _, id := range fl.uses[v] {
			if fl.inLit[id] || l.useCarries(fl, info, id) {
				out[i] = true
				break
			}
		}
	}
	l.carrying[f] = out
	return out
}

// useCarries reports whether the use id of a parameter carries its value
// on (above).
func (l *lowerer) useCarries(fl *flow, info *check.Info, id *syntax.Ident) bool {
	var cur syntax.Node = id
	p := fl.parent[cur]
	for {
		if _, ok := p.(*syntax.ParenExpr); !ok {
			break
		}
		cur, p = p, fl.parent[p]
	}
	switch p := p.(type) {
	case *syntax.ReturnStmt:
		return true
	case *syntax.ExprStmt:
		_, tail := info.Tails[p]
		return tail
	case *syntax.CallExpr:
		c := info.Calls[p]
		if c == nil {
			return true
		}
		if syntax.Node(p.Fun) == cur {
			return false // a call of it: a function value, not a union
		}
		j := operandIndex(p, c, cur)
		if j < 0 {
			return true // a trailing block's or an unlisted operand
		}
		return l.passCarries(c, j)
	case *syntax.AssignStmt:
		// A whole value bound to another name is followed no further: it
		// carries. A store into a field or an element consumes it.
		for i, r := range p.Rhs {
			if syntax.Node(r) == cur && i < len(p.Lhs) {
				_, name := unparen(p.Lhs[i]).(*syntax.Ident)
				return name
			}
		}
	case *syntax.VarSpec:
		return true
	}
	return false
}
