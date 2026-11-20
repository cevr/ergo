package lower

// IR builder: a call of a tabulated decision (TAB-IR; tabulate.go, VI1).
// A call the emitter reads through the function's table reads it on IR
// too, by the same rule (tabTable) and the same index as tabAt:
//
//	a0 := arg0; …               each argument once, in order
//	i := 0; i = i*n0 + int(a0.tag) | int(a0) | (i *= 2; if a0 { i += 1 })
//	tabFRead(i, a0, …)          tabFK[i] ready: tabFV[i]; else tabFFill(i, a0, …)
//
// The table and its functions are Go text in the generated part
// (tabDecl), which the IR names by a call only: a package split gives an
// IR body of another package the table's variables through getters,
// whose results are no places (the 20e3b4fbe fallout: &tabExplainK[i] in
// lzpack's main, "address of a non-place"), and a function through a
// forwarder, which Go inlines as it inlines tabFRead.
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
	readArgs := []*irExpr{local(i)}
	for _, a := range as {
		readArgs = append(readArgs, local(a))
	}
	return out, &irExpr{K: "call", Pos: p, Name: tab.read, Args: readArgs}
}
