package lower

// IR builder: match (IRG3). See ir.go and ir_union.go.
//
// A match on one subject of a union with type tests, wildcards and a
// single `T as x` per case (simpleTypeMatch) lowers as the emitter's
// typeMatch does, by the union's representation (flatOf):
//
//   - a sealed interface: a Go type switch ("tswitch") on the subject,
//     read once into a local; `T as x` asserts the case's type;
//   - a flat union: an expression switch ("switch") on the local's tag,
//     a case per variant's tag; `T as x` reads the variant's slot.
//
// A match on a literal subject (valueMatch) is an expression switch on the
// subject with a case of constants per arm. Each arm yields as an if's
// branch does (tailTo): the function's result, a local (a match in value
// position), or nothing. A match whose value is returned or assigned and
// that has no case _ gets the emitter's defect default (noCase); a
// checked match is total, so no value reaches it.
//
// Not yet: a match the decision tree lowers (patterns other than type
// tests, several subjects), a match on a scalar-form local or on m.Get(k).

import (
	"strconv"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// matchStmt is the match s; ret and to are tailTo's: its arms yield the
// function's result (ret), into to, or nothing.
func (b *irBuilder) matchStmt(s syntax.Stmt, ret bool, to *yieldTo) []*irStmt {
	if b.l.info.Matches[s] == nil {
		irNo("match without its Matches entry")
	}
	switch s := s.(type) {
	case *syntax.TypeSwitchStmt:
		return b.typeMatch(s, ret, to)
	case *syntax.SwitchStmt:
		return b.valueMatch(s, ret, to)
	}
	irNo("statement %T", s)
	return nil
}

// matchValue is the match s in a value position: a local of its type,
// declared first, and the match yielding into it (ifValue).
func (b *irBuilder) matchValue(s syntax.Expr) ([]*irStmt, *irExpr) {
	t, ok := b.l.info.Types[s]
	if !ok {
		irNo("expression without a type")
	}
	p := b.pos(s, s.Pos())
	i := b.temp("m"+strconv.Itoa(b.next), b.goType(t, s), p)
	pre := append([]*irStmt{{K: "var", Pos: p, Local: i}}, b.matchStmt(s.(syntax.Stmt), true, &yieldTo{local: i, t: t})...)
	return pre, &irExpr{K: "local", Pos: p, I: i}
}

// typeMatch is the match s on one subject of a union.
func (b *irBuilder) typeMatch(s *syntax.TypeSwitchStmt, ret bool, to *yieldTo) []*irStmt {
	x := s.Subjects[0].X
	if call, ok := unparen(x).(*syntax.CallExpr); ok {
		if c := b.l.info.Calls[call]; c != nil && c.Kind == check.CallFunc && preludeGet(c.Func) {
			irNo("match on Get")
		}
	}
	if !b.fc.simpleTypeMatch(s) {
		subjects := make([]syntax.Expr, len(s.Subjects))
		for i, g := range s.Subjects {
			if len(b.l.info.Releases[g]) > 0 {
				irNo("match subject that releases owned values")
			}
			subjects[i] = g.X
		}
		return b.treeMatch(s, subjects, s.Clauses, s.Rbrace, ret, to) // ir_tree.go
	}
	// A parameter takes the scalar form only in a body its call is inlined
	// into (scalar.go); the IR does not inline.
	if len(b.l.info.Releases[s.Subjects[0]]) > 0 || len(b.l.info.Releases[x]) > 0 {
		irNo("match subject that releases owned values")
	}
	st := b.l.info.Types[x]
	if st == nil {
		irNo("expression without a type")
	}
	gst := b.goType(st, x)
	p := b.pos(s, s.Pos())
	var pre []*irStmt
	subj := -1
	fu := b.flatOf(st)
	sc := b.scalarIdent(x) // ir_scalar.go
	if sc == nil {
		var sx *irExpr
		pre, sx = b.value(x)
		p = past(p, pre)
		// The subject is read once, into a local the cases read again.
		subj = b.temp("s"+strconv.Itoa(b.next), gst, p)
		pre = append(pre, &irStmt{K: "def", Pos: p, Local: subj, X: sx})
		if fu == nil && !b.fc.isInterface(st) {
			irNo("match on a value that is no union")
		}
	}
	seen := map[string]bool{}
	sw := &irStmt{K: "tswitch", Pos: p, End: b.pos(s, s.Rbrace)}
	switch {
	case sc != nil:
		// A scalar-form local: the switch reads its tag, each case its
		// slot (the emitter's scalarTags).
		sw.K, sw.X = "switch", &irExpr{K: "local", Pos: p, I: sc.tag}
	case fu != nil:
		sw.K, sw.X = "switch", b.tagOf(subj, p)
	default:
		sw.X = &irExpr{K: "local", Pos: p, I: subj}
	}
	hasDefault := false
	for _, cl := range s.Clauses {
		cp := b.pos(cl, cl.Case)
		c := &irCase{Pos: cp}
		def := len(cl.List) == 0
		var as *check.Var
		var asType check.Type
		for _, item := range cl.List {
			pt := b.l.info.Patterns[item]
			if pt == nil {
				irNo("case without its pattern")
			}
			var t check.Type
			switch pt.Kind {
			case check.PatWildcard:
				def = true
				continue
			case check.PatTypeTest:
				t = pt.Type
			case check.PatAs:
				t, as, asType = pt.Elems[0].Type, pt.Var, pt.Elems[0].Type
			}
			g := b.goType(t, item)
			if seen[g] {
				irNo("covered case") // the emitter refuses it (unreachable)
			}
			seen[g] = true
			if sc != nil {
				k := b.scalarTag(sc, t, item)
				if k < 0 {
					irNo("case of another type")
				}
				c.Vals = append(c.Vals, &irExpr{K: "const", Pos: cp, T: "uint8", V: strconv.Itoa(k)})
				continue
			}
			if fu == nil {
				c.Types = append(c.Types, g)
				continue
			}
			k := fu.tagOf(t)
			if k < 0 {
				irNo("case of another type")
			}
			c.Vals = append(c.Vals, &irExpr{K: "const", Pos: cp, T: "uint8", V: strconv.Itoa(k)})
		}
		if as != nil {
			name := b.named(as.Name, b.goType(asType, cl), cp)
			if _, dup := b.locals[as]; dup {
				irNo("local declared twice")
			}
			b.locals[as] = name
			var val *irExpr
			if sc != nil {
				val = &irExpr{K: "local", Pos: cp, I: sc.slots[b.scalarTag(sc, asType, cl)]}
			} else if fu != nil {
				val = b.narrow(fu, subj, fu.tagOf(asType), cp, cl)
			} else {
				val = &irExpr{K: "assert", Pos: cp, T: b.goType(asType, cl), X: &irExpr{K: "local", Pos: cp, I: subj}}
			}
			c.Body = append(c.Body, &irStmt{K: "def", Pos: cp, Local: name, X: val})
		}
		c.Body = append(c.Body, b.arm(cl.Body, ret, to)...)
		if def {
			// The emitter writes default: for the whole case.
			if hasDefault {
				irNo("two defaults")
			}
			hasDefault = true
			c.Def, c.Types, c.Vals = true, nil, nil
		}
		sw.Cases = append(sw.Cases, c)
	}
	return append(pre, b.closeMatch(s, sw, hasDefault, ret, to))
}

// past is p, or the last end of pre's statements when it comes after p: a
// match's switch opens its scope after its subject's scopes close (a
// `match f() catch {...} {...}` puts the catch's switch first), since the
// compiler's DWARF scope positions must not decrease.
func past(p irPos, pre []*irStmt) irPos {
	for _, s := range pre {
		for _, e := range []irPos{s.End, s.ElseEnd} {
			if e[0] == p[0] && (e[1] > p[1] || e[1] == p[1] && e[2] > p[2]) {
				p = e
			}
		}
	}
	return p
}

// tagOf is the tag of the flat union in the local s (tagExpr).
func (b *irBuilder) tagOf(s int, p irPos) *irExpr {
	return &irExpr{K: "field", Pos: p, Name: "tag", X: &irExpr{K: "local", Pos: p, I: s}}
}

// valueMatch is the match s on a literal subject: an expression switch.
func (b *irBuilder) valueMatch(s *syntax.SwitchStmt, ret bool, to *yieldTo) []*irStmt {
	if s.Tag == nil {
		irNo("match without a subject")
	}
	if len(b.l.info.Releases[s.Tag]) > 0 {
		irNo("match subject that releases owned values")
	}
	tt := b.l.info.Types[s.Tag]
	if tt == nil {
		irNo("expression without a type")
	}
	if b.scalarBasic(tt) == nil {
		irNo("match on a value of type %s", tt)
	}
	p := b.pos(s, s.Pos())
	pre, tag := b.value(s.Tag)
	p = past(p, pre)
	sw := &irStmt{K: "switch", Pos: p, End: b.pos(s, s.Rbrace), X: tag}
	seen := map[string]bool{}
	hasDefault := false
	for _, cl := range s.Clauses {
		cp := b.pos(cl, cl.Case)
		c := &irCase{Pos: cp}
		def := len(cl.List) == 0
		for _, item := range cl.List {
			pt := b.l.info.Patterns[item]
			if pt == nil {
				irNo("case without its pattern")
			}
			switch pt.Kind {
			case check.PatWildcard:
				def = true
			case check.PatConst:
				key := caseKey(pt.Type, pt.Value)
				if seen[key] {
					irNo("covered case") // the emitter refuses it (unreachable)
				}
				seen[key] = true
				c.Vals = append(c.Vals, b.constant(tt, pt.Value, item, b.pos(item, item.Pos())))
			default:
				irNo("value case that is not a literal")
			}
		}
		c.Body = b.arm(cl.Body, ret, to)
		if def {
			// The emitter writes default: for the whole case.
			if hasDefault {
				irNo("two defaults")
			}
			hasDefault = true
			c.Def, c.Vals = true, nil
		}
		sw.Cases = append(sw.Cases, c)
	}
	return append(pre, b.closeMatch(s, sw, hasDefault, ret, to))
}

// arm is a case's body: it yields as an if's branch does.
func (b *irBuilder) arm(list []syntax.Stmt, ret bool, to *yieldTo) []*irStmt {
	b.switches++
	defer func() { b.switches-- }()
	if len(list) == 0 {
		if ret || to != nil {
			irNo("empty case with a result")
		}
		return nil
	}
	return b.tailTo(list, ret && to == nil, to)
}

// closeMatch gives sw the emitter's defect default (noCase) when the match
// has no case _ and its value is returned or assigned (needsDefault; a
// checked match is total, so the default runs for no value).
func (b *irBuilder) closeMatch(s syntax.Stmt, sw *irStmt, hasDefault, ret bool, to *yieldTo) *irStmt {
	if hasDefault || !ret && to == nil {
		if !hasDefault {
			sw.ElsePos = sw.End
		}
		return sw
	}
	path, line, col := b.l.locate(s, s.Pos())
	at := strconv.Itoa(line) + ":" + strconv.Itoa(col)
	if _, ok := b.l.sourceOf(s); !ok {
		at = path + ":" + at
	}
	sw.ElsePos = sw.End
	msg := &irExpr{K: "const", Pos: sw.End, T: "string", V: "ergo: defect: no case of the match at " + at + " matched"}
	sw.Else = []*irStmt{{K: "panic", Pos: sw.End, X: msg}}
	return sw
}
