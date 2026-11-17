package lower

// IR builder: match fusion (IR-FORMS IRF-1). A match on a call of a small
// function whose every result is a variant built where it returns, such as
// `match dense.Unpack(id) { case Some as held: … case None: }`, costs a call,
// a union built through memory and a dispatch on its tag in the two-step
// form, for a value whose variant the callee's own control flow already
// knows at each return.
//
// fuseMatch writes the callee's body at the match site, its parameters
// bound to the call's operands, and makes each of its results a jump: the
// result's variant names the match arm it reaches (case-of-known-variant),
// its value goes into that arm's `as` local (or is discarded, so a literal
// that panics still panics), and a goto enters the arm. Every arm is a
// join point: a labeled block after the body, which each result that
// reaches it jumps to, so an arm two results reach is built once (GHC's
// join points; the match tree's shared arms, ir_tree.go). The results may
// be in the callee's tail (an if, a nested match, a block) or in a return
// anywhere in its body (case-of-case through the callee's branches). No
// union value is built.
//
// It applies to a call of any package's function, a generic instance (its
// type parameters spelled as the call's type arguments) or a method call,
// when:
//   - the callee has an Ergo body with no extern, no failure row, no
//     capability, no owned or witness parameter, one result of the
//     match's union type, and no loop or function literal (a loop
//     amortizes its call; a literal's return is its own);
//   - its body has at most fuseBudget syntax nodes (the site's cost: the
//     body is written at every fused site);
//   - each result is a variant the checker converts into the union (a
//     result of the union's own type, a call returning it, keeps the
//     two-step form);
//   - the match's cases are type tests, `T as x` or `case _`;
//   - the census is not running, and the site is not inside another fused
//     body (one level: a fused body's own matches keep their form).
//
// Anything the builder refuses inside (irNo) restores the builder and
// builds the two-step form. The callee's nodes take the match site's
// position (the compiler's DWARF scopes must not go back to its lines);
// each arm keeps its case's position, in the match's order.
//
// viewMatch (ir_view.go, VW1) is tried first: it builds a view's switch as
// the match's own, with its lets sunk into the cases (VW1b), which a join
// after the body cannot do.
//
// The reference is fuseLowering "off": the two-step form.
// TestIRFusesMatchesOnCalls (irspike) runs seeded fusions through the IR
// path and the source path.
//
// Prior art: GHC's case-of-known-constructor and case-of-case with join
// points (Maurer et al., "Compiling without continuations"); OCaml's
// flambda unboxes a variant it sees built at a match; ReScript's Lambda
// simplifier folds a switch on a known block tag after inlining; Elixir's
// sys_core_fold folds a case on a known tuple. Gleam leaves the value to
// the host.

import (
	"maps"
	"strconv"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// fuseLowering is "off" for the reference: no match is fused.
var fuseLowering string

// fuseBudget is the most syntax nodes a callee's body has to be written at
// a fused site: dense.Unpack is about 40.
const fuseBudget = 256

// fuseSite is a match fused with the body of the call it matches on.
type fuseSite struct {
	byType   map[string]*syntax.CaseClause // each tested variant's arm, by Go type
	rest     *syntax.CaseClause            // case _
	variants map[string]bool               // the union's variants' Go types
	label    map[*syntax.CaseClause]string
	bind     map[*syntax.CaseClause]int // an arm's `as` local, declared before the body
	bindType map[*syntax.CaseClause]string
	reached  map[*syntax.CaseClause]bool
}

// saveBuilder is a restore of the builder's per-site state (viewMatch's
// and fuseMatch's way back to the two-step form).
func (b *irBuilder) saveBuilder() (restore func(), locals map[*check.Var]int) {
	next, nLocals, at := b.next, len(b.fn.Locals), b.at
	locals, admitted, switches := maps.Clone(b.locals), maps.Clone(b.admitted), b.switches
	hoisted, entry := maps.Clone(b.hoisted), len(b.entry)
	given, label, labels := maps.Clone(b.given), b.label, b.labels
	return func() {
		b.next, b.fn.Locals, b.at = next, b.fn.Locals[:nLocals], at
		b.locals, b.admitted, b.switches = locals, admitted, switches
		b.hoisted, b.entry = hoisted, b.entry[:entry]
		b.given, b.label, b.labels = given, label, labels
		b.fuse = nil
	}, locals
}

// fuseMatch is the match s fused with the body of the call it matches on,
// and true; or false, with the builder as it was, for the two-step form.
func (b *irBuilder) fuseMatch(s *syntax.TypeSwitchStmt, ret bool, to *yieldTo) (out []*irStmt, ok bool) {
	if fuseLowering == "off" || b.st.census != nil || b.fuse != nil || len(s.Subjects) != 1 {
		return nil, false
	}
	restore, locals := b.saveBuilder()
	defer func() {
		if r := recover(); r != nil {
			if _, isNo := r.(irUnsupported); !isNo {
				panic(r)
			}
			restore()
			out, ok = nil, false
		}
	}()
	call, c, fz := b.fuseOf(s)
	if fz == nil {
		restore()
		return nil, false
	}
	out = b.fuseBuild(s, call, c, fz, ret, to)
	// The callee's locals and the arms' are the site's alone.
	b.locals = locals
	return out, true
}

// fuseOf is the call s matches on and its fusion, or nil.
func (b *irBuilder) fuseOf(s *syntax.TypeSwitchStmt) (*syntax.CallExpr, *check.Call, *fuseSite) {
	if len(b.l.info.Releases[s.Subjects[0]]) > 0 || len(b.l.info.Releases[s.Subjects[0].X]) > 0 {
		return nil, nil, nil
	}
	call, ok := unparen(s.Subjects[0].X).(*syntax.CallExpr)
	if !ok || call.Trailing != nil {
		return nil, nil, nil
	}
	c := b.l.info.Calls[call]
	if c == nil || c.Kind != check.CallFunc || c.Func == nil || c.StartsTask || c.Site != nil {
		return nil, nil, nil
	}
	f := c.Func
	if f.Pkg == nil || f.Decl == nil || f.Decl.Extern != nil || f.Decl.Body == nil || f.Decl.Recv != nil || f.Ptr {
		return nil, nil, nil
	}
	sig := f.Sig
	if b.l.intrinsic(f) != nil || len(rowElems(sig.Errors)) > 0 || len(rowElems(sig.Uses)) > 0 || len(sig.Results) != 1 {
		return nil, nil, nil
	}
	if len(b.l.ownedParams(f)) > 0 || len(witnessParams(sig)) > 0 || len(c.TypeArgs) != len(sig.TParams) {
		return nil, nil, nil
	}
	for _, tp := range sig.TParams {
		if tp.Kind != check.KindType {
			return nil, nil, nil
		}
	}
	if inlineSize(f.Decl.Body) > fuseBudget || len(f.Decl.Body.List) == 0 {
		return nil, nil, nil
	}
	plain := true
	syntax.Inspect(f.Decl.Body, func(n syntax.Node) bool {
		switch n.(type) {
		case *syntax.FuncLit, *syntax.ForStmt, *syntax.RangeStmt:
			plain = false
		}
		return plain
	})
	if !plain {
		return nil, nil, nil
	}
	params := paramVars(f)
	args := call.Args
	if c.Recv != nil {
		args = append([]syntax.Expr{c.Recv}, args...)
	}
	if len(params) != len(args) || len(params) == 0 && len(sig.Params) > 0 {
		return nil, nil, nil
	}
	for i, a := range args {
		if params[i] == nil || len(b.l.info.Releases[a]) > 0 {
			return nil, nil, nil
		}
	}
	n, ok := b.l.info.Types[s.Subjects[0].X].(*check.Named)
	if !ok {
		return nil, nil, nil
	}
	u, ok := n.Underlying().(*check.Union)
	if !ok {
		return nil, nil, nil
	}
	fz := &fuseSite{byType: map[string]*syntax.CaseClause{}, variants: map[string]bool{}, label: map[*syntax.CaseClause]string{},
		bind: map[*syntax.CaseClause]int{}, bindType: map[*syntax.CaseClause]string{}, reached: map[*syntax.CaseClause]bool{}}
	for _, t := range u.Variants {
		fz.variants[b.goType(t, s)] = true
	}
	for _, cl := range s.Clauses {
		if len(cl.List) == 0 {
			fz.rest = cl
		}
		for _, item := range cl.List {
			pt := b.l.info.Patterns[item]
			if pt == nil {
				return nil, nil, nil
			}
			switch pt.Kind {
			case check.PatWildcard:
				fz.rest = cl
			case check.PatTypeTest:
				fz.byType[b.goType(pt.Type, item)] = cl
			case check.PatAs:
				fz.byType[b.goType(pt.Elems[0].Type, item)] = cl
			default:
				return nil, nil, nil
			}
		}
	}
	return call, c, fz
}

// fuseBuild builds the match s fused with the call's body.
func (b *irBuilder) fuseBuild(s *syntax.TypeSwitchStmt, call *syntax.CallExpr, c *check.Call, fz *fuseSite, ret bool, to *yieldTo) []*irStmt {
	f := c.Func
	p := b.pos(s, s.Pos())
	end := b.pos(s, s.Rbrace)
	b.at = &p
	var pre []*irStmt
	params := paramVars(f)
	args := call.Args
	if c.Recv != nil {
		args = append([]syntax.Expr{c.Recv}, args...)
	}
	// The type arguments, spelled in the caller.
	targs := make([]string, len(c.TypeArgs))
	for i, t := range c.TypeArgs {
		targs[i] = b.fc.goType(t)
		b.goType(t, call)
	}
	// The operands, left to right: each goes into a local of its
	// parameter's type (a Go call's copy) once the callee's types are
	// spelled, or is the caller's immutable local itself.
	argLocal := make([]int, len(args))
	vals := make([]*irExpr, len(args))
	for i, a := range args {
		if l, ok := b.sameLocal(a); ok {
			argLocal[i] = l
			continue
		}
		apre, e := b.value(a)
		pre = append(pre, apre...)
		argLocal[i], vals[i] = -1, e
	}
	// Each arm's label, and its `as` local, declared before the body.
	var decls []*irStmt
	for _, cl := range s.Clauses {
		b.labels++
		fz.label[cl] = "fuse" + strconv.Itoa(b.labels)
		for _, item := range cl.List {
			pt := b.l.info.Patterns[item]
			if pt == nil || pt.Kind != check.PatAs {
				continue
			}
			if _, dup := b.locals[pt.Var]; dup {
				irNo("local declared twice")
			}
			g := b.goType(pt.Elems[0].Type, item)
			l := b.temp(pt.Var.Name, g, p)
			b.locals[pt.Var] = l
			fz.bind[cl], fz.bindType[cl] = l, g
			decls = append(decls, &irStmt{K: "var", Pos: p, Local: l})
		}
	}
	b.labels++
	exit := "fuse" + strconv.Itoa(b.labels)
	var body []*irStmt
	func() {
		defer b.l.leave(b.l.enter(f.Decl.Name, f.Pkg))
		if f.Pkg.Info != b.l.info {
			// enter keeps the info of a package with no source of its own
			// (std): the callee's facts are its package's (inline.go).
			saved := b.l.info
			b.l.info = f.Pkg.Info
			defer func() { b.l.info = saved }()
		}
		if b.fc.params == nil {
			b.fc.params = map[*check.TypeParam]string{}
		}
		for i, tp := range f.Sig.TParams {
			b.fc.params[tp] = targs[i]
		}
		defer func() {
			for _, tp := range f.Sig.TParams {
				delete(b.fc.params, tp)
			}
		}()
		k := len(b.fn.Locals)
		for i, a := range args {
			pv := params[i]
			if argLocal[i] >= 0 {
				b.locals[pv] = argLocal[i]
				continue
			}
			l := b.temp(pv.Name, b.goType(pv.Type, a), p)
			b.locals[pv] = l
			pre = append(pre, &irStmt{K: "def", Pos: p, Local: l, X: vals[i]})
		}
		b.fuse = fz
		defer func() { b.fuse = nil }()
		body = b.tailTo(f.Decl.Body.List, false, &yieldTo{local: -1, t: f.Sig.Results[0], fuse: true})
		b.rename(k)
	}()
	outer := append(decls, &irStmt{K: "block", Pos: p, End: p, Body: body})
	b.at = nil
	jumps := 0
	var reached []*syntax.CaseClause
	for _, cl := range s.Clauses {
		if fz.reached[cl] {
			reached = append(reached, cl)
		}
	}
	if len(reached) == 0 {
		irNo("fused body that reaches no arm")
	}
	for k, cl := range reached {
		cp := b.pos(cl, cl.Case)
		arm := b.arm(cl.Body, ret, to)
		if k < len(reached)-1 && !(len(arm) > 0 && irTerminates(arm[len(arm)-1])) && !ret {
			arm = append(arm, &irStmt{K: "goto", Pos: end, Label: exit})
			jumps++
		}
		outer = append(outer, &irStmt{K: "block", Pos: cp, End: end, Label: fz.label[cl], Body: arm})
	}
	if jumps > 0 {
		outer = append(outer, &irStmt{K: "block", Pos: end, End: end, Label: exit})
	}
	out := append(pre, &irStmt{K: "block", Pos: p, End: end, Body: outer})
	cur := p
	monotone(out, &cur)
	return out
}

// fuseTail is a result x of a fused body (b.fuse): its value into the arm
// its variant reaches, then a jump to that arm.
func (b *irBuilder) fuseTail(x syntax.Expr, p irPos) []*irStmt {
	fz := b.fuse
	t := b.l.info.Types[x]
	conv, ok := b.l.info.Implicit[x]
	if !ok || conv.Kind != check.ConvVariant {
		irNo("fused result that is no variant put into its union")
	}
	t = conv.From
	if t == nil {
		irNo("expression without a type")
	}
	g := b.goType(t, x)
	if !fz.variants[g] {
		irNo("fused result of another union")
	}
	arm := fz.byType[g]
	if arm == nil {
		arm = fz.rest
	}
	if arm == nil {
		irNo("fused result that no arm takes")
	}
	fz.reached[arm] = true
	pre, e := b.variant(x) // ir_view.go: no union is built
	if l, ok := fz.bind[arm]; ok {
		if fz.bindType[arm] != g {
			irNo("fused result of another variant than its arm binds")
		}
		pre = append(pre, &irStmt{K: "set", Pos: p, Local: l, X: e})
	} else {
		pre = append(pre, &irStmt{K: "discard", Pos: p, X: e})
	}
	return append(pre, &irStmt{K: "goto", Pos: p, Label: fz.label[arm]})
}
