package lower

// IR builder: a call of a tabulated decision (TAB-IR; tabulate.go, VI1).
// A call the emitter reads through the function's table reads it on IR
// too, by the same rule (tabTable) and the same statements as tabAt:
//
//	a0 := arg0; …               each argument once, in order
//	i := 0; i = i*n0 + int(a0.tag) | int(a0) | (i *= 2; if a0 { i += 1 })
//	var v R
//	if tabFK[i].Ready() { v = tabFV[i] } else { v = tabFFill(i, a0, …) }
//
// The table, its flags and its fill function are Go text in the
// generated part (tabDecl), which the IR names as globals and as a call.
// The reference is tabLowering "call", where no call is tabulated on
// either path; vt's ClassOf(byte) is the counted case (VT-PROFILE).

import (
	"strconv"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// tabCall is the read of the call x (c) of a tabulated function with the
// built arguments args, and its value; nil when the call takes the
// function itself.
func (b *irBuilder) tabCall(x *syntax.CallExpr, c *check.Call, args []*irExpr, p irPos) ([]*irStmt, *irExpr) {
	tab := b.fc.tabTable(c, len(args))
	if tab == nil {
		return nil, nil
	}
	local := func(i int) *irExpr { return &irExpr{K: "local", Pos: p, I: i} }
	num := func(n int) *irExpr { return &irExpr{K: "const", Pos: p, T: "int", V: strconv.Itoa(n)} }
	var out []*irStmt
	as := make([]int, len(args))
	for k, a := range args {
		as[k] = b.temp("a", b.goType(tab.info.params[k].t, x), p)
		out = append(out, &irStmt{K: "def", Pos: p, Local: as[k], X: a})
	}
	i := b.temp("i", "int", p)
	out = append(out, &irStmt{K: "def", Pos: p, Local: i, X: num(0)})
	for k, pa := range tab.info.params {
		scaled := &irExpr{K: "bin", Pos: p, Op: "*", X: local(i), Y: num(pa.n)}
		switch pa.kind {
		case "enum":
			tag := &irExpr{K: "conv", Pos: p, T: "int", X: &irExpr{K: "field", Pos: p, Name: "tag", X: local(as[k])}}
			out = append(out, &irStmt{K: "set", Pos: p, Local: i, X: &irExpr{K: "bin", Pos: p, Op: "+", X: scaled, Y: tag}})
		case "byte":
			v := &irExpr{K: "conv", Pos: p, T: "int", X: local(as[k])}
			out = append(out, &irStmt{K: "set", Pos: p, Local: i, X: &irExpr{K: "bin", Pos: p, Op: "+", X: scaled, Y: v}})
		case "bool":
			out = append(out,
				&irStmt{K: "set", Pos: p, Local: i, X: &irExpr{K: "bin", Pos: p, Op: "*", X: local(i), Y: num(2)}},
				&irStmt{K: "if", Pos: p, End: p, X: local(as[k]), Then: []*irStmt{
					{K: "set", Pos: p, Local: i, X: &irExpr{K: "bin", Pos: p, Op: "+", X: local(i), Y: num(1)}},
				}})
		default:
			irNo("tabulated parameter of kind %s", pa.kind)
		}
	}
	v := b.temp("v", b.goType(c.Func.Sig.Results[0], x), p)
	at := func(name string) *irExpr {
		return &irExpr{K: "index", Pos: p, X: &irExpr{K: "global", Pos: p, Name: name}, Y: local(i)}
	}
	ready := &irExpr{K: "mcall", Pos: p, Name: "Ready", X: &irExpr{K: "addr", Pos: p, X: at(tab.flags)}}
	fillArgs := []*irExpr{local(i)}
	for _, a := range as {
		fillArgs = append(fillArgs, local(a))
	}
	out = append(out,
		&irStmt{K: "var", Pos: p, Local: v},
		&irStmt{K: "if", Pos: p, End: p, ElsePos: p, ElseEnd: p, X: ready,
			Then: []*irStmt{{K: "set", Pos: p, Local: v, X: at(tab.vals)}},
			Else: []*irStmt{{K: "set", Pos: p, Local: v, X: &irExpr{K: "call", Pos: p, Name: tab.fill, Args: fillArgs}}},
		})
	return out, local(v)
}
