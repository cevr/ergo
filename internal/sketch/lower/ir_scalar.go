package lower

// IR builder: union locals in the scalar form (IRG3b). See ir.go and
// scalar.go.
//
// The lowering decides which locals take the scalar form (scalarVars,
// fc.scalarOf), and the builder reads that decision: a loop-carried union
// local lives as IR locals of its own, its tag (uint8) and one slot per
// variant, as the emitter writes it:
//
//   - its let and each plain assignment take the union value apart by a
//     type switch into the tag and the variant's slot (scatter), with the
//     emitter's defect default;
//   - a simple type match on it switches on the tag, and each case reads
//     its variant's slot (the emitter's scalarTags);
//   - any other read puts the slot of its tag into a union value of its
//     own (gather).
//
// A join local (a let, not mut, of a call that a match takes) takes the
// form in the emitter only when the call is inlined; the IR does not
// inline, so it is a plain local, as the emitter's non-inlined case.

import (
	"strconv"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// irScalar is a local in the scalar form: the IR locals of its tag and
// slots, its union type and its variants, by tag.
type irScalar struct {
	tag      int
	slots    []int
	union    check.Type
	variants []check.Type
}

// scalarLet is the let of v, of the value x, in the scalar form when the
// lowering gives v that form here; ok false when v is a plain local.
func (b *irBuilder) scalarLet(v *check.Var, x syntax.Expr, mut bool, p irPos) (out []*irStmt, ok bool) {
	if b.fc.scalarOf(v) == nil || joinCall(x) != nil && !mut {
		return nil, false
	}
	n, isNamed := v.Type.(*check.Named)
	if !isNamed {
		irNo("scalar-form local of an unnamed type")
	}
	u, isUnion := n.Underlying().(*check.Union)
	if !isUnion {
		irNo("scalar-form local of a non-union")
	}
	b.goType(v.Type, x)
	pre, e := b.value(x)
	sc := &irScalar{union: v.Type, variants: u.Variants}
	sc.tag = b.temp(v.Name, "uint8", p)
	for _, vt := range u.Variants {
		sc.slots = append(sc.slots, b.temp(v.Name, b.goType(vt, x), p))
	}
	out = append(out, pre...)
	out = append(out, &irStmt{K: "var", Pos: p, Local: sc.tag})
	for _, s := range sc.slots {
		out = append(out, &irStmt{K: "var", Pos: p, Local: s})
	}
	out = append(out, b.scatter(sc, e, x, p)...)
	if b.scalars == nil {
		b.scalars = map[*check.Var]*irScalar{}
	}
	b.scalars[v] = sc
	return out, true
}

// scatter writes the union value e into sc: a type switch on it, a case
// per variant setting the tag and the slot, and the emitter's defect
// default.
func (b *irBuilder) scatter(sc *irScalar, e *irExpr, site syntax.Node, p irPos) []*irStmt {
	gu := b.goType(sc.union, site)
	x := b.temp("x"+strconv.Itoa(b.next), gu, p)
	sw := &irStmt{K: "tswitch", Pos: p, End: p, ElsePos: p, X: &irExpr{K: "local", Pos: p, I: x}}
	for k, vt := range sc.variants {
		gv := b.goType(vt, site)
		sw.Cases = append(sw.Cases, &irCase{Pos: p, Types: []string{gv}, Body: []*irStmt{
			{K: "set", Pos: p, Local: sc.tag, X: &irExpr{K: "const", Pos: p, T: "uint8", V: strconv.Itoa(k)}},
			{K: "set", Pos: p, Local: sc.slots[k], X: &irExpr{K: "assert", Pos: p, T: gv, X: &irExpr{K: "local", Pos: p, I: x}}},
		}})
	}
	msg := &irExpr{K: "const", Pos: p, T: "string", V: "ergo: defect: a value of " + gu + " that is none of its variants"}
	sw.Else = []*irStmt{{K: "panic", Pos: p, X: msg}}
	return []*irStmt{{K: "def", Pos: p, Local: x, X: e}, sw}
}

// gather is the union value of sc in an IR local of its own: the slot of
// its tag.
func (b *irBuilder) gather(sc *irScalar, site syntax.Node, p irPos) ([]*irStmt, *irExpr) {
	g := b.temp("g"+strconv.Itoa(b.next), b.goType(sc.union, site), p)
	sw := &irStmt{K: "switch", Pos: p, End: p, ElsePos: p, X: &irExpr{K: "local", Pos: p, I: sc.tag}}
	for k, vt := range sc.variants {
		val := b.variantInto(vt, sc.union, &irExpr{K: "local", Pos: p, I: sc.slots[k]}, site)
		sw.Cases = append(sw.Cases, &irCase{Pos: p, Vals: []*irExpr{{K: "const", Pos: p, T: "uint8", V: strconv.Itoa(k)}}, Body: []*irStmt{{K: "set", Pos: p, Local: g, X: val}}})
	}
	return []*irStmt{{K: "var", Pos: p, Local: g}, sw}, &irExpr{K: "local", Pos: p, I: g}
}

// scalarIdent is the scalar local x names, or nil.
func (b *irBuilder) scalarIdent(x syntax.Expr) *irScalar {
	if b.scalars == nil {
		return nil
	}
	id, ok := unparen(x).(*syntax.Ident)
	if !ok {
		return nil
	}
	v, ok := b.l.info.Uses[id].(*check.Var)
	if !ok {
		return nil
	}
	return b.scalars[v]
}

// scalarTag is the tag of variant t in sc, or -1.
func (b *irBuilder) scalarTag(sc *irScalar, t check.Type, site syntax.Node) int {
	g := b.goType(t, site)
	for k, vt := range sc.variants {
		if b.goType(vt, site) == g {
			return k
		}
	}
	return -1
}
