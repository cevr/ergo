package lower

// irTreeCopy is the one deep copy of IR statements and expressions, each
// node with every fact it carries (MATCH-BIND-ICE r2). Every IR copier
// goes through it: the inliner's clone of a body or a callee
// (ir_forms_inline.go) and a task block's copy for GoWith (ir_spawn.go).
//
// The facts a node carries, and what a copy does with each:
//   - irStmt.F and Was (irMatch): an arm's Bind is a statement of the
//     tree and Subj a local, so the copy's facts are re-keyed to the
//     copy (irMatch.copyFor): its binds the copy's statements, its
//     subject renamed by subj. Sharing them made verifyMatch fail
//     (examples/search's census on the REASON-REACH scratch).
//   - irExpr.F and Was (irVariant: a tag, a layout, a method name),
//     C (irCall: the source call and its callee) and CF (irCallFacts:
//     the source call, why it is kept, whether in statement position):
//     values that name no node of the tree and no local, never written
//     after the node is built, so the copy shares them.
//   - irExpr.FT, irStmt.Ls, Key and Val, irExpr.Params, Keys and End:
//     copied, so a rename or a type substitution of the copy never
//     writes the original.
//   - The builder's side tables keyed by a node: irInline.sites (the
//     inliner's call sites) is re-keyed by the inliner's node hook; a
//     copy for GoWith keeps no site, so a call in a task block is not
//     inlined (an optimization not taken, never a wrong body).
//     irBuilder.dropsErr is read while a call is built, before any copy;
//     storeFielder.splice and the exits pass's regs live in one pass over
//     one tree and are never copied.
type irTreeCopy struct {
	// expr, when it returns a node, stands for e in the copy (a callee's
	// parameter, a task block's captured local); nil copies e.
	expr func(e *irExpr) *irExpr
	// local renames a local of the copy; nil keeps it.
	local func(int) int
	// subj is a match form's subject local in the copy (-1: none); nil is
	// local.
	subj func(int) int
	// stmt, cas and node finish a copied statement, case and expression:
	// positions, labels, types and side tables. Each may be nil.
	stmt func(old, n *irStmt)
	cas  func(old, n *irCase)
	node func(old, n *irExpr)
}

func (c *irTreeCopy) renamed(i int) int {
	if c.local == nil {
		return i
	}
	return c.local(i)
}

func (c *irTreeCopy) stmts(list []*irStmt) []*irStmt {
	if list == nil {
		return nil
	}
	out := make([]*irStmt, len(list))
	for i, s := range list {
		out[i] = c.stmtOf(s)
	}
	return out
}

func (c *irTreeCopy) stmtOf(s *irStmt) *irStmt {
	if s == nil {
		return nil
	}
	n := *s
	n.X, n.Y = c.exprOf(s.X), c.exprOf(s.Y)
	n.Then, n.Else, n.Body = c.stmts(s.Then), c.stmts(s.Else), c.stmts(s.Body)
	if s.Cases != nil {
		n.Cases = make([]*irCase, len(s.Cases))
		for i, k := range s.Cases {
			nk := *k
			nk.Body = c.stmts(k.Body)
			nk.Vals = c.exprs(k.Vals)
			if k.Types != nil {
				nk.Types = append([]string(nil), k.Types...)
			}
			if c.cas != nil {
				c.cas(k, &nk)
			}
			n.Cases[i] = &nk
		}
	}
	subj := c.subj
	if subj == nil {
		subj = c.renamed
	}
	n.F, n.Was = s.F.copyFor(s.Cases, n.Cases, subj), s.Was.copyFor(s.Cases, n.Cases, subj) // ir_forms.go
	switch s.K {
	case "def", "set", "var", "def2", "mapget", "count":
		n.Local = c.renamed(s.Local)
	}
	if s.Key != nil {
		k := c.renamed(*s.Key)
		n.Key = &k
	}
	if s.Val != nil {
		v := c.renamed(*s.Val)
		n.Val = &v
	}
	if s.Ls != nil {
		n.Ls = make([]int, len(s.Ls))
		for i, l := range s.Ls {
			n.Ls[i] = c.renamed(l)
		}
	}
	if c.stmt != nil {
		c.stmt(s, &n)
	}
	return &n
}

func (c *irTreeCopy) exprs(list []*irExpr) []*irExpr {
	if list == nil {
		return nil
	}
	out := make([]*irExpr, len(list))
	for i, e := range list {
		out[i] = c.exprOf(e)
	}
	return out
}

func (c *irTreeCopy) exprOf(e *irExpr) *irExpr {
	if e == nil {
		return nil
	}
	if c.expr != nil {
		if r := c.expr(e); r != nil {
			return r
		}
	}
	n := *e
	n.X, n.Y = c.exprOf(e.X), c.exprOf(e.Y)
	n.Args, n.Elems = c.exprs(e.Args), c.exprs(e.Elems)
	n.Body = c.stmts(e.Body)
	if e.Keys != nil {
		n.Keys = append([]string(nil), e.Keys...)
	}
	if e.Params != nil {
		n.Params = make([]int, len(e.Params))
		for i, p := range e.Params {
			n.Params[i] = c.renamed(p)
		}
	}
	if e.End != nil {
		p := *e.End
		n.End = &p
	}
	if e.FT != nil {
		n.FT = append([]string(nil), e.FT...)
	}
	if e.K == "local" {
		n.I = c.renamed(e.I)
	}
	if c.node != nil {
		c.node(e, &n)
	}
	return &n
}
