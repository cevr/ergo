package lower

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// mapOp lowers the language's Get, Put and Take on a Go map. These
// operations have no Ergo body to declare, including when the prelude
// itself is the root. The value rule copies a map before a write unless
// the call can take it; the lookup returns the program's Option.
func (b *irBuilder) mapOp(x *syntax.CallExpr, c *check.Call, p irPos, stmt bool) ([]*irStmt, *irExpr, bool) {
	list := x.Args
	if c.Recv != nil {
		list = append([]syntax.Expr{c.Recv}, list...)
	}
	pre, args := b.operands(list)
	b.valueSource(x, c, list, args)
	p = past(p, pre)
	m, k := args[0], args[1]
	mt := b.l.info.Types[list[0]]
	local := func(i int) *irExpr { return &irExpr{K: "local", Pos: p, I: i} }
	op := mapOpOf(c.Func)
	if op == mapOpPut {
		i := b.temp("m", b.goType(mt, x), p)
		put := &irStmt{K: "store", Pos: p, X: &irExpr{K: "mapAt", Pos: p, X: local(i), Y: k}, Y: args[2]}
		pre = append(pre, &irStmt{K: "def", Pos: p, Local: i, X: m}, put)
		if stmt {
			return pre, nil, false
		}
		return pre, local(i), false
	}
	result := b.l.info.Types[x]
	option := result
	if op == mapOpTake {
		option = result.(*check.Tuple).Types[0]
	}
	v := b.temp("value", b.goType(optionElem(option), x), p)
	found := b.temp("found", "bool", p)
	if op == mapOpTake {
		// Both the lookup and deletion read the same operands, once.
		mi, ki := b.temp("m", b.goType(mt, x), p), b.temp("k", b.goType(c.TypeArgs[0], x), p)
		pre = append(pre, &irStmt{K: "def", Pos: p, Local: mi, X: m}, &irStmt{K: "def", Pos: p, Local: ki, X: k})
		m, k = local(mi), local(ki)
		if !valueRule() {
			// The alias reference still gives Take a fresh remainder.
			mp, key, elem := b.goType(mt, x), b.goType(c.TypeArgs[0], x), b.goType(c.TypeArgs[1], x)
			ri := b.temp("rest", mp, p)
			pre = append(pre, &irStmt{K: "tdef", Pos: p, Ls: []int{v, found, ri}, X: &irExpr{K: "pcall", Pos: p, T: runtimeImport, Name: "MapTake", V: key + ", " + elem, Args: []*irExpr{m, k}}})
			m = local(ri)
		}
	}
	if op != mapOpTake || valueRule() {
		pre = append(pre, &irStmt{K: "mapget", Pos: p, Local: v, Val: &found, X: m, Y: k})
		if op == mapOpTake {
			pre = append(pre, &irStmt{K: "expr", Pos: p, X: &irExpr{K: "builtin", Pos: p, Name: "delete", Args: []*irExpr{m, k}}})
		}
	}
	if stmt {
		return pre, nil, false
	}
	out := b.temp("entry", b.goType(option, x), p)
	some, _ := optionVariants(option, x)
	lit := &irExpr{K: "lit", Pos: p, T: b.goType(some, x), Keys: []string{memberName("Value")}, Elems: []*irExpr{local(v)}}
	pre = append(pre,
		&irStmt{K: "def", Pos: p, Local: out, X: b.machineNone(option, p, x)},
		&irStmt{K: "if", Pos: p, End: p, X: local(found), Then: []*irStmt{{K: "set", Pos: p, Local: out, X: b.variantInto(some, option, lit, x)}}})
	if op == mapOpTake {
		return pre, b.tupleLit(result.(*check.Tuple), []*irExpr{local(out), m}, x, p), false
	}
	return pre, local(out), false
}
