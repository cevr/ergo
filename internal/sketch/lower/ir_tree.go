package lower

// IR builder: match trees (IRG3b). See ir.go and ir_match.go.
//
// A match that is not a simple type match (record, tuple, constant, bind
// and nested patterns, several subjects) lowers as the emitter lowers it:
// the emitter's tree builder (matchtree.go, fc.matchTree) decides the
// decision tree once, and the IR builder writes that tree, so the two
// paths test the same values in the same order. The builder runs the
// emitter's tree on placeholder values: each subject is read once into an
// IR local, named "\x01s<local>", and a column the tree reads is that name
// followed by the Go field names it selects (".F0" of a tuple), or a
// type switch's narrowed name (fc.temp("n")) followed by fields. The IR
// writes:
//
//   - a type switch node on an interface column: a "tswitch" whose case
//     binds the narrowed value by an assertion;
//   - a type switch node on a flat union column (flatOf): a "switch" on
//     its tag, whose case binds the variant's slot (narrow);
//   - a constant node: a "switch" on the column, a case per constant;
//   - a leaf: the arm's bindings as locals, then the arm's body; an arm
//     that several leaves reach is a labeled block after the tree, whose
//     bindings are declared before the tree and set by each leaf before
//     its goto (the emitter's static exception);
//   - a default branch that no row reaches: the noCase defect when the
//     match's value is used (closeMatch), else nothing.
//
// The tree visits arms in the order its tests decide, not in source
// order, and the compiler's DWARF scopes must open at positions that do
// not decrease (dwarfgen's marker): monotone moves a scope's position up
// to the last one written before it.
//
// A scalar-form local as a subject is gathered first, as the emitter's
// headerValue does (ir_scalar.go). Not yet: a match the tree leaves to the
// flat lowering.

import (
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// treeIR writes one match tree.
type treeIR struct {
	b *irBuilder
	// s is the match or the catch; site a syntax expression of it, where
	// a constant's type is admitted.
	s    syntax.Node
	site syntax.Expr
	p    irPos
	// body writes arm k's body; exit the statements of a default branch
	// that no row reaches (none: the branch is not written); terminates
	// reports that every arm's body ends the function.
	body       func(k int) []*irStmt
	exit       func(p irPos) []*irStmt
	terminates bool
	// heads are the IR locals of the tree's value names: the subjects'
	// placeholders and the narrowed names of the cases being written.
	heads map[string]treeHead
	// types are the checked types the tree's case labels name, by Go
	// type; consts the constants its labels spell, by their Go text.
	types  map[string]check.Type
	consts map[string]*check.Pattern
	// prefix are the statements before the switch being written.
	prefix []*irStmt
	// shared are the labels of the arms that several leaves reach.
	shared map[int]string
}

type treeHead struct {
	local int
	t     check.Type
}

// treeMatch is the match s on subjects whose arms are clauses, by the
// emitter's decision tree.
func (b *irBuilder) treeMatch(s syntax.Stmt, subjects []syntax.Expr, clauses []*syntax.CaseClause, end syntax.Pos, ret bool, to *yieldTo) []*irStmt {
	p := b.pos(s, s.Pos())
	var pre []*irStmt
	var cols []treeHead
	for _, x := range subjects {
		if len(b.l.info.Releases[x]) > 0 {
			irNo("match subject that releases owned values")
		}
		st := b.l.info.Types[x]
		if st == nil {
			irNo("expression without a type")
		}
		gst := b.goType(st, x)
		xp, e := b.value(x)
		pre = append(pre, xp...)
		p = past(p, pre)
		local := b.temp("s"+strconv.Itoa(b.next), gst, p)
		pre = append(pre, &irStmt{K: "def", Pos: p, Local: local, X: e})
		cols = append(cols, treeHead{local, st})
	}
	alts := make([][]*check.Pattern, len(clauses))
	for k, cl := range clauses {
		for _, item := range cl.List {
			pt := b.l.info.Patterns[item]
			if pt == nil {
				irNo("case without its pattern")
			}
			alts[k] = append(alts[k], pt)
		}
	}
	t := &treeIR{b: b, s: s, site: subjects[0], p: p, terminates: ret && to == nil}
	t.body = func(k int) []*irStmt { return b.arm(clauses[k].Body, ret, to) }
	if ret || to != nil {
		t.exit = func(p irPos) []*irStmt { return []*irStmt{noCase(b, s, p)} }
	}
	return append(pre, t.write(cols, alts, b.pos(s, end))...)
}

// write is the tree of the columns cols (IR locals and their checked
// types) and the arms' pattern alternatives alts (none: an arm that
// matches every value), the match ending at ep.
func (t *treeIR) write(cols []treeHead, alts [][]*check.Pattern, ep irPos) []*irStmt {
	b, s, p := t.b, t.s, t.p
	t.heads, t.types, t.consts = map[string]treeHead{}, map[string]check.Type{}, map[string]*check.Pattern{}
	vals := make([]string, len(cols))
	types := make([]check.Type, len(cols))
	for i, c := range cols {
		vals[i] = "\x01s" + strconv.Itoa(c.local)
		types[i] = c.t
		t.heads[vals[i]] = c
	}
	arms := make([]treeArm, len(alts))
	for k, a := range alts {
		arms[k].alts = a
		for _, pt := range a {
			t.collect(pt)
		}
	}
	tree := b.fc.matchTree(s, vals, types, arms)
	if tree == nil {
		irNo("match with patterns") // the flat lowering (flatPatternMatch)
	}
	leaves := make([]int, len(arms))
	first := make([]*mleaf, len(arms))
	tree.walk(func(n *mnode) {
		if n.leaf != nil {
			if leaves[n.leaf.arm] == 0 {
				first[n.leaf.arm] = n.leaf
			}
			leaves[n.leaf.arm]++
		}
	})
	// An arm that several leaves reach is a labeled block after the tree;
	// its bindings are declared before the tree, and each leaf sets them
	// and jumps to it (the emitter's emitMatchTree).
	var decls []*irStmt
	var shared []int
	t.shared = map[int]string{}
	for arm, k := range leaves {
		if k < 2 {
			continue
		}
		shared = append(shared, arm)
		b.labels++
		t.shared[arm] = "arm" + strconv.Itoa(b.labels)
		for _, bd := range first[arm].binds {
			if _, dup := b.locals[bd.v]; dup {
				irNo("local declared twice")
			}
			l := b.named(bd.v.Name, b.goType(bd.v.Type, s), p)
			b.locals[bd.v] = l
			decls = append(decls, &irStmt{K: "var", Pos: p, Local: l})
		}
	}
	out := t.child(tree, ep) // a match whose first row tests nothing is a leaf
	if len(shared) > 0 {
		// Every arm returns when the match's value is the function's
		// result; otherwise each path leaves the block at its end.
		exit := ""
		if !t.terminates {
			b.labels++
			exit = "end" + strconv.Itoa(b.labels)
		}
		body := append(decls, out...)
		jump := func() {
			if exit != "" {
				body = append(body, &irStmt{K: "goto", Pos: ep, Label: exit})
			}
		}
		jump()
		for k, arm := range shared {
			body = append(body, &irStmt{K: "block", Pos: p, End: ep, Label: t.shared[arm], Body: t.body(arm)})
			if k < len(shared)-1 {
				jump()
			}
		}
		if exit != "" {
			body = append(body, &irStmt{K: "block", Pos: ep, End: ep, Label: exit})
		}
		out = []*irStmt{{K: "block", Pos: p, End: ep, Body: body}}
	}
	cur := p
	monotone(out, &cur)
	return out
}

// collect records the types and constants of the pattern p and its parts.
func (t *treeIR) collect(p *check.Pattern) {
	switch p.Kind {
	case check.PatTypeTest, check.PatRecord:
		t.types[t.b.fc.goType(p.Type)] = p.Type
	case check.PatConst:
		t.consts[t.b.fc.constant(p.Type, p.Value, t.site)] = p
	}
	for _, e := range p.Elems {
		t.collect(e)
	}
	for _, f := range p.Fields {
		t.collect(f.Pattern)
	}
}

// value is the IR value of the tree's value name v, and its checked type.
func (t *treeIR) value(v string) (*irExpr, check.Type) {
	parts := strings.Split(v, ".")
	h, ok := t.heads[parts[0]]
	if !ok {
		irNo("match tree value %q", v)
	}
	e, typ := &irExpr{K: "local", Pos: t.p, I: h.local}, h.t
	for _, f := range parts[1:] {
		typ = t.fieldType(typ, f)
		e = &irExpr{K: "field", Pos: t.p, Name: f, X: e}
	}
	return e, typ
}

// fieldType is the type of the field whose Go name is f in a value of the
// record or tuple type t.
func (t *treeIR) fieldType(typ check.Type, f string) check.Type {
	switch u := underlying(typ).(type) {
	case *check.Tuple:
		for i, et := range u.Types {
			if tupleField(i) == f {
				return et
			}
		}
	case *check.Struct:
		n, ok := typ.(*check.Named)
		if !ok {
			irNo("record pattern of an unnamed type")
		}
		td := t.b.l.typeDecl(n.Obj, t.s)
		for _, sf := range u.Fields {
			if g, ok := td.fieldName[sf.Name]; ok && g == f {
				return sf.Type
			}
		}
	}
	irNo("match tree field %s of %s", f, typ)
	return nil
}

// node writes the node n; end is the match's closing brace.
func (t *treeIR) node(n *mnode, end irPos) *irStmt {
	b := t.b
	p := t.p

	col, ct := t.value(n.operand)
	sw := &irStmt{K: "switch", Pos: p, End: end, X: col}
	var fu *flatUnion
	if n.typeSwitch {
		if col.K != "local" {
			// The cases read the column again: read it once.
			l := b.temp("c"+strconv.Itoa(b.next), b.goType(ct, t.s), p)
			t.prefix = append(t.prefix, &irStmt{K: "def", Pos: p, Local: l, X: col})
			col = &irExpr{K: "local", Pos: p, I: l}
			sw.X = col
		}
		fu = b.flatOf(ct)
		if fu != nil {
			sw.X = b.tagOf(col.I, p)
		} else {
			if !b.fc.isInterface(ct) {
				irNo("match tree type switch on a value that is no union")
			}
			sw.K = "tswitch"
		}
	}
	for _, c := range n.cases {
		ic := &irCase{Pos: p}
		if n.typeSwitch {
			vt, ok := t.types[c.label]
			if !ok {
				irNo("match tree case of an unknown type")
			}
			var bound *irExpr
			if fu != nil {
				k := fu.tagOfGo(&b.fc.typeCtx, c.label)
				if k < 0 {
					irNo("case of another type")
				}
				ic.Vals = []*irExpr{{K: "const", Pos: p, T: "uint8", V: strconv.Itoa(k)}}
				bound = b.narrow(fu, col.I, k, p, t.s)
			} else {
				ic.Types = []string{c.label}
				bound = &irExpr{K: "assert", Pos: p, T: c.label, X: col}
			}
			l := b.temp(n.bound, b.goType(vt, t.s), p)
			ic.Body = append(ic.Body, &irStmt{K: "def", Pos: p, Local: l, X: bound})
			t.heads[n.bound] = treeHead{l, vt}
		} else {
			cp, ok := t.consts[c.label]
			if !ok {
				irNo("match tree case of an unknown constant")
			}
			ic.Vals = []*irExpr{b.constant(ct, cp.Value, t.site, p)}
		}
		ic.Body = append(ic.Body, t.child(c.child, end)...)
		sw.Cases = append(sw.Cases, ic)
	}
	delete(t.heads, n.bound)
	sw.ElsePos = end
	switch {
	case !n.def.fail:
		sw.Cases = append(sw.Cases, &irCase{Pos: p, Def: true, Body: t.child(n.def, end)})
	case t.exit != nil:
		sw.Else = t.exit(end)
	}
	return sw
}

// child is the statements of the node n below a case.
func (t *treeIR) child(n *mnode, end irPos) []*irStmt {
	if n.fail {
		irNo("match tree that fails outside a default branch") // the emitter's ice
	}
	if n.leaf != nil {
		return t.leaf(n.leaf)
	}
	saved := t.prefix
	t.prefix = nil
	sw := t.node(n, end)
	out := append(t.prefix, sw)
	t.prefix = saved
	return out
}

// leaf binds the leaf's values and writes its arm's body.
func (t *treeIR) leaf(lf *mleaf) []*irStmt {
	b := t.b
	var out []*irStmt
	if label, ok := t.shared[lf.arm]; ok {
		for _, bd := range lf.binds {
			e, et := t.value(bd.val)
			l, ok := b.locals[bd.v]
			if !ok {
				irNo("shared arm binding that its first leaf does not make")
			}
			if b.goType(et, t.s) != b.goType(bd.v.Type, t.s) {
				irNo("match binding of another type")
			}
			out = append(out, &irStmt{K: "set", Pos: t.p, Local: l, X: e})
		}
		return append(out, &irStmt{K: "goto", Pos: t.p, Label: label})
	}
	for _, bd := range lf.binds {
		e, et := t.value(bd.val)
		g := b.goType(bd.v.Type, t.s)
		if b.goType(et, t.s) != g {
			irNo("match binding of another type")
		}
		if _, dup := b.locals[bd.v]; dup {
			irNo("local declared twice")
		}
		l := b.named(bd.v.Name, g, t.p)
		b.locals[bd.v] = l
		out = append(out, &irStmt{K: "def", Pos: t.p, Local: l, X: e})
	}
	return append(out, t.body(lf.arm)...)
}

// noCase is the emitter's defect of a match that no case matched, at the
// position p (closeMatch).
func noCase(b *irBuilder, s syntax.Stmt, p irPos) *irStmt {
	path, line, col := b.l.locate(s, s.Pos())
	at := strconv.Itoa(line) + ":" + strconv.Itoa(col)
	if _, ok := b.l.sourceOf(s); !ok {
		at = path + ":" + at
	}
	msg := &irExpr{K: "const", Pos: p, T: "string", V: "ergo: defect: no case of the match at " + at + " matched"}
	return &irStmt{K: "panic", Pos: p, X: msg}
}

// monotone moves the scope positions of list up, in the order the
// compiler marks them, so that none comes before the one marked before
// it: a switch's position, each case's, its default's (ElsePos), its end;
// an if's position, its then block, its end, its else block; a loop's
// position, body and end. cur is the last position marked.
func monotone(list []*irStmt, cur *irPos) {
	up := func(p *irPos) {
		if *p == (irPos{}) {
			return
		}
		if posBefore(*p, *cur) {
			*p = *cur
		}
		*cur = *p
	}
	for _, s := range list {
		switch s.K {
		case "switch", "tswitch":
			up(&s.Pos)
			for _, c := range s.Cases {
				up(&c.Pos)
				monotone(c.Body, cur)
			}
			if len(s.Else) > 0 {
				up(&s.ElsePos)
				monotone(s.Else, cur)
			}
			up(&s.End)
		case "if":
			up(&s.Pos)
			monotone(s.Then, cur)
			up(&s.End)
			if len(s.Else) > 0 {
				up(&s.ElsePos)
				monotone(s.Else, cur)
				up(&s.ElseEnd)
			}
		default:
			if s.End != (irPos{}) {
				up(&s.Pos)
				monotone(s.Then, cur)
				monotone(s.Body, cur)
				up(&s.End)
			}
		}
	}
}

// posBefore reports a before b in one file.
func posBefore(a, b irPos) bool {
	if a[0] != b[0] {
		return false
	}
	return a[1] < b[1] || a[1] == b[1] && a[2] < b[2]
}
