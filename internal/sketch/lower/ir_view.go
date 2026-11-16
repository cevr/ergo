package lower

// IR builder: one dispatch for a match on a view (VW1). See ir_match.go.
//
// A view function reads a row as a variant: its body is lets, then one
// match on a literal subject whose every arm builds one variant of its
// result, a literal `V{…}` or a field-less variant's name (lane B's B2 view
// walk: ViewOf(t, id) switches on a row's kind over 60 kinds). A match
// whose subject is a call of a view, `match ViewOf(t, id) { case V as x: … }`,
// costs two dispatches per row in the two-step form: the view's switch on
// the kind, then the match's on the variant's tag, with the union between
// them through memory (the view is past Go's inlining budget).
//
// viewMatch builds such a match as one switch: the view's arguments into
// locals, its lets, then its switch on the kind, where each arm builds its
// variant into the match arm that variant reaches (its `as` local, or
// discarded when the arm binds none, so a literal that panics still
// panics) and goes on with that arm's body. No union value is built. The
// view's own body still gets its IR body for its other callers.
//
// It applies when, besides the shape:
//   - the view is a plain function of the match's package: no extern, no
//     type parameters, no rows, one result, no owned or witness
//     parameters, every parameter named, at most viewBudget syntax nodes;
//   - a match arm two of the view's arms reach is copied into each case,
//     up to viewShare syntax nodes of copies per site;
//   - the census is not running (it records the body as the two-step form
//     builds it).
//
// Anything the builder refuses inside the view (irNo) restores the
// builder and builds the two-step form, so a view never costs its caller
// its IR body. The view's nodes take the match site's position (at): the
// compiler's DWARF scopes must not go back to the view's lines. The cases
// keep the match's arm order and positions; a Go switch on constants does
// not depend on the order of its cases.
//
// The reference is viewLowering "off" (-ldflags -X …lower.viewLowering=off):
// the two-step form. TestIRViewsMatchSource (irspike) runs seeded views
// through the IR path and the source path, whose two-step form is the
// reference.
//
// Prior art: GHC's case-of-case moves the outer case into each branch of
// the inner one once the inner is inlined (join points share an arm two
// branches reach) and then cancels the known constructor; OCaml's flambda
// and MoonBit match a constructor they can see at its source. Gleam,
// Elixir and ReScript leave the inner value to the host (a tagged tuple, a
// JS object) and its JIT, which may or may not see through it.

import (
	"maps"
	"slices"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// viewLowering is "off" for the reference: a match on a view call is built
// in the two-step form.
var viewLowering string

// viewBudget is the most syntax nodes a view's body has to be built at a
// match site: B2's 60-kind view is about 1,500.
const viewBudget = 2048

// viewShare is the most syntax nodes of match arms copied, summed over a
// site: a match arm two of the view's arms reach is built in each of their
// cases (case _ over the variants no other arm tests, `case A, B`). GHC
// shares such an arm through a join point; a Go switch has no jump into a
// case, so a small arm is copied and a larger one keeps the two-step form.
const viewShare = 64

// viewSite is a match on a view call that viewMatch builds as one switch.
type viewSite struct {
	args   []syntax.Expr
	params []*check.Var
	lets   []syntax.Stmt
	inner  *syntax.SwitchStmt
	// to is the match arm each of the view's arms reaches.
	to map[*syntax.CaseClause]*syntax.CaseClause
}

// viewMatch is the match s on a view call as one switch, and true; or
// false, with the builder as it was, for the two-step form.
func (b *irBuilder) viewMatch(s *syntax.TypeSwitchStmt, ret bool, to *yieldTo) (out []*irStmt, ok bool) {
	if viewLowering == "off" || b.st.census != nil || len(s.Subjects) != 1 {
		return nil, false
	}
	next, nLocals, at := b.next, len(b.fn.Locals), b.at
	locals, admitted, switches := maps.Clone(b.locals), maps.Clone(b.admitted), b.switches
	restore := func() {
		b.next, b.fn.Locals, b.at = next, b.fn.Locals[:nLocals], at
		b.locals, b.admitted, b.switches = locals, admitted, switches
	}
	defer func() {
		if r := recover(); r != nil {
			if _, isNo := r.(irUnsupported); !isNo {
				panic(r)
			}
			restore()
			out, ok = nil, false
		}
	}()
	v := b.viewOf(s)
	if v == nil {
		restore()
		return nil, false
	}
	out = b.viewSwitch(s, v, ret, to)
	// The view's locals and the arms' are the switch's alone.
	b.locals = locals
	return out, true
}

// viewOf is the view call s matches on, or nil.
func (b *irBuilder) viewOf(s *syntax.TypeSwitchStmt) *viewSite {
	if len(b.l.info.Releases[s.Subjects[0]]) > 0 || len(b.l.info.Releases[s.Subjects[0].X]) > 0 {
		return nil
	}
	call, ok := unparen(s.Subjects[0].X).(*syntax.CallExpr)
	if !ok || call.Trailing != nil {
		return nil
	}
	c := b.l.info.Calls[call]
	if c == nil || c.Kind != check.CallFunc || c.Func == nil || c.StartsTask || len(c.TypeArgs) > 0 || c.Site != nil {
		return nil
	}
	f := c.Func
	if f.Pkg == nil || f.Pkg.Info != b.l.info || f.Decl == nil || f.Decl.Extern != nil || f.Decl.Body == nil || f.Decl.Recv != nil || f.Ptr {
		return nil
	}
	if b.l.intrinsic(f) != nil || !plainSig(f.Sig) || len(f.Sig.Results) != 1 || len(b.l.ownedParams(f)) > 0 || len(witnessParams(f.Sig)) > 0 {
		return nil
	}
	if inlineSize(f.Decl.Body) > viewBudget {
		return nil
	}
	v := &viewSite{args: call.Args, params: paramVars(f), to: map[*syntax.CaseClause]*syntax.CaseClause{}}
	if c.Recv != nil {
		v.args = append([]syntax.Expr{c.Recv}, v.args...)
	}
	if len(v.params) != len(v.args) || slices.Contains(v.params, nil) {
		return nil
	}
	for _, a := range v.args {
		if len(b.l.info.Releases[a]) > 0 {
			return nil
		}
	}
	list := f.Decl.Body.List
	if len(list) == 0 {
		return nil
	}
	for _, st := range list[:len(list)-1] {
		if _, ok := st.(*syntax.DeclStmt); !ok {
			return nil
		}
	}
	v.lets = list[:len(list)-1]
	v.inner, ok = list[len(list)-1].(*syntax.SwitchStmt)
	if !ok || v.inner.Tag == nil || len(v.inner.Clauses) == 0 {
		return nil
	}
	// The match's arms by the Go type of the variant each tests.
	n, ok := b.l.info.Types[s.Subjects[0].X].(*check.Named)
	if !ok {
		return nil
	}
	u, ok := n.Underlying().(*check.Union)
	if !ok {
		return nil
	}
	variants := map[string]bool{}
	for _, t := range u.Variants {
		variants[b.goType(t, s)] = true
	}
	byType := map[string]*syntax.CaseClause{}
	var rest *syntax.CaseClause
	for _, cl := range s.Clauses {
		if len(cl.List) == 0 {
			rest = cl
		}
		for _, item := range cl.List {
			pt := b.l.info.Patterns[item]
			if pt == nil {
				return nil
			}
			switch pt.Kind {
			case check.PatWildcard:
				rest = cl
			case check.PatTypeTest:
				byType[b.goType(pt.Type, item)] = cl
			case check.PatAs:
				byType[b.goType(pt.Elems[0].Type, item)] = cl
			default:
				return nil
			}
		}
	}
	reached := map[*syntax.CaseClause]bool{}
	copies := 0
	for _, cl := range v.inner.Clauses {
		if len(cl.Body) != 1 {
			return nil
		}
		e, ok := cl.Body[0].(*syntax.ExprStmt)
		if !ok {
			return nil
		}
		// The arm's value goes into the view's result: the checker records
		// that as the variant's conversion to the union, the only one taken.
		t := b.l.info.Types[e.X]
		if conv, ok := b.l.info.Implicit[e.X]; ok {
			if conv.Kind != check.ConvVariant {
				return nil
			}
			t = conv.From
		}
		switch x := unparen(e.X).(type) {
		case *syntax.CompositeLit:
		case *syntax.Ident:
			// A field-less variant's name, not a local of the union.
			if _, ok := b.l.info.Uses[x].(*check.TypeName); !ok {
				return nil
			}
		default:
			return nil
		}
		if t == nil {
			return nil
		}
		g := b.goType(t, e.X)
		if !variants[g] {
			return nil
		}
		arm := byType[g]
		if arm == nil {
			arm = rest
		}
		if arm == nil {
			return nil
		}
		if reached[arm] {
			// A second view arm reaching this match arm gets a copy of it.
			copies += inlineSize(arm)
			if copies > viewShare {
				return nil
			}
		}
		reached[arm] = true
		v.to[cl] = arm
	}
	return v
}

// viewSwitch builds the match s on the view v as one switch.
func (b *irBuilder) viewSwitch(s *syntax.TypeSwitchStmt, v *viewSite, ret bool, to *yieldTo) []*irStmt {
	p := b.pos(s, s.Pos())
	end := b.pos(s, s.Rbrace)
	b.at = &p
	var pre []*irStmt
	for i, a := range v.args {
		apre, e := b.value(a)
		pre = append(pre, apre...)
		l := b.temp(v.params[i].Name, b.goType(v.params[i].Type, a), p)
		pre = append(pre, &irStmt{K: "def", Pos: p, Local: l, X: e})
		b.locals[v.params[i]] = l
	}
	k := len(b.fn.Locals)
	for _, st := range v.lets {
		pre = append(pre, b.stmt(st)...)
	}
	b.rename(k)
	order := map[*syntax.CaseClause]int{}
	for i, cl := range s.Clauses {
		order[cl] = i
	}
	var arms []*syntax.CaseClause
	var at []irPos
	spre, sw, hasDefault := b.valueArms(v.inner, func(cl *syntax.CaseClause) []*irStmt {
		arm := v.to[cl]
		// The arm's locals are its case's: a copy of the arm declares
		// them again.
		saved := maps.Clone(b.locals)
		defer func() { b.locals = saved }()
		b.at = nil
		cp := b.pos(arm, arm.Case)
		arms, at = append(arms, arm), append(at, cp)
		b.at = &cp
		lit := cl.Body[0].(*syntax.ExprStmt).X
		k := len(b.fn.Locals)
		body, e := b.variant(lit)
		b.rename(k)
		var as *check.Var
		var asType check.Type
		for _, item := range arm.List {
			if pt := b.l.info.Patterns[item]; pt != nil && pt.Kind == check.PatAs {
				as, asType = pt.Var, pt.Elems[0].Type
			}
		}
		if as != nil {
			if _, dup := b.locals[as]; dup {
				irNo("local declared twice")
			}
			name := b.temp(as.Name, b.goType(asType, arm), cp)
			b.locals[as] = name
			body = append(body, &irStmt{K: "def", Pos: cp, Local: name, X: e})
		} else {
			body = append(body, &irStmt{K: "discard", Pos: cp, X: e})
		}
		b.at = nil
		body = append(body, b.arm(arm.Body, ret, to)...)
		b.at = &p
		return body
	})
	b.at = nil
	for i, c := range sw.Cases {
		c.Pos = at[i]
	}
	idx := make([]int, len(sw.Cases))
	for i := range idx {
		idx[i] = i
	}
	slices.SortStableFunc(idx, func(i, j int) int { return order[arms[i]] - order[arms[j]] })
	cases := make([]*irCase, len(idx))
	for i, k := range idx {
		cases[i] = sw.Cases[k]
	}
	sw.Cases, sw.Pos, sw.End = cases, p, end
	// The view's match is total by its own check: with no case _, a kind
	// no arm takes is its defect, as in the two-step form.
	pre = append(pre, spre...)
	return append(pre, b.closeMatch(v.inner, sw, hasDefault, true, nil))
}

// variant is the value of a view arm's x as its variant: its conversion
// to the view's union, which the checker records at x, is left out while
// x is built, since no union is built.
func (b *irBuilder) variant(x syntax.Expr) ([]*irStmt, *irExpr) {
	if conv, ok := b.l.info.Implicit[x]; ok {
		delete(b.l.info.Implicit, x)
		defer func() { b.l.info.Implicit[x] = conv }()
	}
	return b.value(x)
}

// rename gives the locals the view's body declared from index k on the
// temporaries' names: the view's `let a` now sits in its caller's scope,
// whose body may spell a, or hold a second copy of the view.
func (b *irBuilder) rename(k int) {
	for i := k; i < len(b.fn.Locals); i++ {
		b.fn.Locals[i].Name = b.names.alloc(b.fn.Locals[i].Name)
	}
}
