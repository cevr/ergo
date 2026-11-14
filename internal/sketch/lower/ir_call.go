package lower

// IR builder: calls, operation calls and polls. See ir.go.

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

func (b *irBuilder) poll(site syntax.Node) *irStmt {
	p := b.pos(site, site.Pos())
	task := &irExpr{K: "field", Pos: p, Name: envTask, X: &irExpr{K: "local", Pos: p, I: b.env}}
	return &irStmt{K: "expr", Pos: p, X: &irExpr{K: "mcall", Pos: p, Name: "Check", X: task}}
}

// call is a call x; stmt reports a call in statement position, where a
// call with no result may stand.
func (b *irBuilder) call(x *syntax.CallExpr, stmt bool) ([]*irStmt, *irExpr) {
	if x.Trailing != nil {
		irNo("trailing block")
	}
	c := b.l.info.Calls[x]
	if c == nil {
		irNo("call without a Calls entry")
	}
	if len(c.TypeArgs) > 0 || c.StartsTask {
		irNo("generic or task call")
	}
	if !stmt && b.l.info.Types[x] == nil {
		irNo("call without a value")
	}
	p := b.pos(x, x.Pos())
	switch c.Kind {
	case check.CallLen:
		if len(x.Args) != 1 {
			irNo("len form")
		}
		switch t := b.l.info.Types[x.Args[0]].(type) {
		case *check.Slice:
		case *check.Basic:
			if t.Kind != check.String {
				irNo("len of a non-slice")
			}
		default:
			irNo("len of a non-slice")
		}
		return nil, &irExpr{K: "len", Pos: p, X: b.expr(x.Args[0])}
	case check.CallFunc:
		f := c.Func
		if f == nil || f.Pkg != b.l.pkg || check.GoBuiltin(f) != "" || userExtern(f) {
			irNo("call of a function outside the package")
		}
		fd := b.l.useFunc(f, x)
		if !fd.pure || fd.fallible || fd.extern != "" || fd.ptr || len(b.l.ownedParams(f)) > 0 || len(witnessParams(f.Sig)) > 0 {
			irNo("call of an impure, fallible or special function")
		}
		args := x.Args
		if c.Recv != nil {
			args = append([]syntax.Expr{c.Recv}, args...)
		}
		var irArgs []*irExpr
		for _, a := range args {
			irArgs = append(irArgs, b.expr(a))
		}
		if fd.method != nil {
			return nil, &irExpr{K: "mcall", Pos: p, Name: fd.name, X: irArgs[0], Args: irArgs[1:]}
		}
		return nil, &irExpr{K: "call", Pos: p, Name: fd.name, Args: irArgs}
	case check.CallProviderOp:
		if b.env < 0 || c.Op == nil || len(rowElems(c.Op.Sig.Errors)) > 0 || b.fc.entersRelease(x) || b.l.tests != nil {
			irNo("operation call form")
		}
		sel, ok := unparen(x.Fun).(*syntax.SelectorExpr)
		if !ok {
			irNo("operation call spelling")
		}
		named, ok := b.l.info.Types[sel.X].(*check.Named)
		if !ok {
			irNo("capability without a named type")
		}
		env := &irExpr{K: "local", Pos: p, I: b.env}
		args := []*irExpr{{K: "field", Pos: p, Name: envTask, X: env}}
		for _, a := range x.Args {
			if _, isCall := unparen(a).(*syntax.CallExpr); isCall {
				irNo("call in an operation's argument")
			}
			args = append(args, b.expr(a))
		}
		var pre []*irStmt
		if b.fc.frame.polls {
			pre = append(pre, b.poll(x))
		}
		capField := &irExpr{K: "field", Pos: p, Name: b.l.envField(named), X: env}
		return pre, &irExpr{K: "mcall", Pos: p, Name: memberName(c.Op.Name), X: capField, Args: args}
	}
	irNo("call kind %d", c.Kind)
	return nil, nil
}
