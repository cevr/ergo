package check

import "ergo.local/prototype/internal/sketch/syntax"

// Local contexts (rows.md, Context 9; spec D-E5). `let x = context { … }` in
// a body binds x to a local context: a recipe that every with naming it
// builds again. It is not a value:
//
//  1. Binding. The context pass makes one *Context per local literal
//     (scanLocals), with its binding summary, so an embedding of a local
//     resolves and is summarised under the package's context budget like an
//     inline context's (D16). Typing binds that object to the let's name in
//     the body's scope, lexically, under the binder rules of every local
//     (D-D2, D-D3). A local may reuse a package context's name; the local
//     wins in its scope (runtime.md, G1 rule 2).
//  2. Checking once. Its provider expressions are checked once, at the let,
//     in the let's lexical scope: their names are the let's. What they
//     charge (capabilities) and let escape (failures) is kept as the
//     context's summary (Context.needs, fails), charged nowhere at the let.
//  3. Charging at each with. A with that names the local context charges
//     the summary at its operand: a capability resolves against the withs
//     around that with and its function's row (S102 there; P4), and a
//     failure escapes to that with's function. A provider never sees its own
//     context's bindings.
//  4. Placement. A local context is used only as a with operand or as an
//     embedding entry of a context literal: any other use is S108
//     (contextValue, a named context's too), and so are a `let mut` of a context, a written
//     type on one, and a context literal anywhere but after `let x =`, as a
//     with operand or in a package-level let.
//
// Prior art: Effect's Layer and ZIO's ZLayer are recipes built at each
// provide, whose requirements are satisfied there; Ergo's local context is
// one, and it has no run-time value at all.

// The S108 messages of a context outside its places (rows.md, Context 9).
const (
	contextValueText   = "a context is used only by with or embedded in a context: with w { … } or context { w, … }"
	contextRebindText  = "a context is bound once by let; it is not reassigned"
	contextTypeText    = "a context has no type to write; bind it with let x = context { … }"
	contextOutsideText = "a context literal stands only after let x = or as a with operand"
)

// localContextLit is the literal a local let binds as a local context, or
// nil: one name other than _, no pattern and one value that is a context
// literal. The context pass and typing both read it, so the two agree on
// which lets bind a local context.
func localContextLit(spec *syntax.VarSpec) *syntax.ContextLit {
	if spec == nil || spec.Pattern != nil || len(spec.Names) != 1 || len(spec.Values) != 1 || spec.Names[0].Name == "_" {
		return nil
	}
	lit, _ := unparen(spec.Values[0]).(*syntax.ContextLit)
	return lit
}

// scanLocals finds, in the top-level declaration d, the local contexts and
// the embedding entries that name one, by the rule typing's scopes apply: a
// let binds from the end of its statement to the end of the statement list
// that holds it (a block, a match arm, a catch arm), and an inner binding
// of a name is found before an outer one. It reads each node once and keeps
// a stack of bindings per name, so it is linear in d.
func (c *checker) scanLocals(fi *fileInfo, d syntax.Node) (map[*syntax.ContextLit]*Context, map[*syntax.ContextEntry]*Context) {
	type binding struct {
		ctx   *Context
		from  syntax.Pos
		depth int
	}
	var (
		locals  map[*syntax.ContextLit]*Context
		embeds  map[*syntax.ContextEntry]*Context
		ends    []syntax.Pos // the ends of the open statement lists
		order   []string     // the names bound, innermost last
		byName  = map[string][]binding{}
		closeTo = func(depth int) {
			for len(order) > 0 {
				name := order[len(order)-1]
				bs := byName[name]
				if bs[len(bs)-1].depth <= depth {
					return
				}
				byName[name] = bs[:len(bs)-1]
				order = order[:len(order)-1]
			}
		}
	)
	syntax.Inspect(d, func(n syntax.Node) bool {
		for len(ends) > 0 && n.Pos() >= ends[len(ends)-1] {
			ends = ends[:len(ends)-1]
			closeTo(len(ends))
		}
		switch n := n.(type) {
		case *syntax.BlockStmt, *syntax.CaseClause, *syntax.CatchArm, *syntax.CommClause:
			ends = append(ends, n.End())
		case *syntax.DeclStmt:
			for _, spec := range n.Decl.Specs {
				lit := localContextLit(spec)
				if lit == nil {
					continue
				}
				id := spec.Names[0]
				w := &Context{Name: id.Name, Pos: id.Pos(), Lit: lit, Local: true, unknown: true, file: fi, pkg: c.pkg}
				if locals == nil {
					locals = map[*syntax.ContextLit]*Context{}
				}
				locals[lit] = w
				byName[id.Name] = append(byName[id.Name], binding{w, n.End(), len(ends)})
				order = append(order, id.Name)
			}
		case *syntax.ContextEntry:
			if n.Kind != syntax.ContextEmbed {
				break
			}
			id, ok := n.Key.(*syntax.Ident)
			if !ok {
				break
			}
			bs := byName[id.Name]
			// Inside a let's own literal its name is not bound yet: the
			// binding below it, if any, is the one in scope.
			for i := len(bs) - 1; i >= 0 && i >= len(bs)-2; i-- {
				if id.Pos() >= bs[i].from {
					if embeds == nil {
						embeds = map[*syntax.ContextEntry]*Context{}
					}
					embeds[n] = bs[i].ctx
					break
				}
			}
		}
		return true
	})
	return locals, embeds
}

// localBinds finishes the body of a local context's literal in the context
// pass, within the allowance prepareBody set and inline charged, and makes
// the context's summary: its embeddings' union with its own entries put
// over it, each charged on m, in entry and key order. Its keys resolve in
// the package's scope, as a named context's do (contextPass.own): a body
// adds no type a key can name. It reports false when the budget ran out.
func (c *checker) localBinds(s *bodySum, lit *syntax.ContextLit, m *contextMeter) bool {
	w := s.local
	savedCur, savedFn := c.cur, c.fn
	c.cur, c.fn = w.file, &funcCtx{scope: newScope(c.pkg.Scope)}
	defer func() { c.cur, c.fn = savedCur, savedFn }()
	s.pending = false
	if !c.finishBody(s, lit.Body, &contextMeter{limit: s.allowance, parent: m}) {
		panic("check: a context body passed the allowance charged for it")
	}
	binds := &capSet{root: s.binds.root, size: s.binds.size}
	for _, n := range s.owns {
		var ok bool
		if binds, ok = binds.put(m, n.t, n.entry); !ok {
			return false
		}
	}
	s.binds, s.owns = binds, nil
	w.binds, w.unknown = binds, !s.known
	return true
}

// localContextLet checks `let x = context { … }` in a body (rows.md, Context
// 9): the context pass made x's context and its binding summary; here its
// provider expressions are checked once, in the let's scope, into the
// context's needs and failures, and x is bound. A `let mut` and a written
// type are S108, and x still binds, so its uses add nothing (D3).
func (c *checker) localContextLet(d *syntax.VarDecl, spec *syntax.VarSpec, lit *syntax.ContextLit) {
	id := spec.Names[0]
	w := c.bodySummary(lit.Body).local
	if w == nil {
		panic("check: a local context the context pass did not make")
	}
	if d.Mut.IsValid() {
		c.errorIn(d, d.Mut, d.Mut+3, CodeType, "%s", contextRebindText)
	}
	if spec.Type != nil {
		c.errorf(spec.Type, CodeType, "%s", contextTypeText)
	}
	// The providers charge a target of their own, with inferred rows and no
	// with around them: what they need and what escapes them is the
	// summary, and nothing is charged at the let. A provider runs where a
	// with builds it, so return, break and continue cannot leave it.
	t := c.fn.t
	sum := &target{name: t.name, errors: newRow(), uses: newRow(), resultKnown: true}
	c.fn.t = sum
	c.fn.branches = append(c.fn.branches, branch{kind: brBarrier})
	c.contextHeader(lit)
	c.contextBody(lit.Body, w)
	c.fn.branches = c.fn.branches[:len(c.fn.branches)-1]
	c.fn.t = t
	w.needs, w.fails = sum.uses, sum.errors
	c.declareContext(id, w)
}

// declareContext binds the local context w to id in the current scope,
// under the binder rules of every local (localNameRules).
func (c *checker) declareContext(id *syntax.Ident, w *Context) {
	c.localNameRules(id)
	c.def(id, w)
	c.fn.scope.insert(id.Name, w)
}

// contextValue reports a context, local or named, used other than as a
// with operand or an embedding (S108): an assignment's target, or any
// value.
func (c *checker) contextValue(x *syntax.Ident) operand {
	if c.writeTarget == syntax.Expr(x) {
		c.errorf(x, CodeType, "%s", contextRebindText)
	} else {
		c.errorf(x, CodeType, "%s", contextValueText)
	}
	return bad(x)
}

// chargeLocal charges, at the with operand at, the summary of the local
// context w that the with builds: each capability its providers need, which
// a with around this one provides or the function's written row declares
// (S102 here otherwise; P4), and each failure they let escape (rows.md,
// Context 9). A need widens no inferred row: the reader of the with sees
// what it builds only in a written row, so a function whose row is
// inferred declares nothing, and its with gets the S102.
func (c *checker) chargeLocal(at syntax.Node, w *Context) {
	if t := c.fn.t; t.declUses == nil && !t.context {
		t.declUses = newRow()
		defer func() { t.declUses = nil }()
	}
	c.chargeRow(at, w.needs)
	c.escape(at, w.fails)
}
