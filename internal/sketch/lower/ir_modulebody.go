package lower

import (
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// moduleCall is the call x of f, an extern that binds an effect-free body
// of the module's own Go (LAWFFI LF2m; check.ModuleBodySource,
// docs/spec/interop.md, Trusted Go 3): a "pcall" of the body by its
// package's import path, each slice operand a copy (slices.Clone), as the
// emitter's runtimeCall passes it, since the body's Go may write the
// slice. The call is a value in its expression: the body is effect-free
// (no host, no package state, no goroutine), so its result is a function
// of its operands. A rewrite law's right side lowers its calls here
// (lawSide), so the IR runs the Go kernel where the law admits it.
func (b *irBuilder) moduleCall(x *syntax.CallExpr, c *check.Call, f *check.Func, p irPos) ([]*irStmt, *irExpr, bool) {
	src, _ := check.ModuleBodySource(f)
	if c.Recv != nil || x.Trailing != nil || len(x.Args) != len(src.Body.Params) || len(f.Sig.Params) != len(x.Args) {
		irNo("call of a module's Go body of another arity")
	}
	pre, args := b.operands(x.Args)
	for i := range src.Body.Params {
		s, ok := f.Sig.Params[i].Type.(*check.Slice)
		if !ok {
			continue
		}
		elem := b.goType(s.Elem, x)
		b.keepImport("var _ = " + b.l.goPkg("slices") + ".Clone[[]byte]")
		args[i] = &irExpr{K: "pcall", Pos: p, T: "slices", Name: "Clone", V: "[]" + elem + ", " + elem, Args: []*irExpr{args[i]}}
	}
	path, name := f.Decl.Extern.Split()
	if strings.Contains(name, ".") {
		irNo("call of a module's Go method")
	}
	b.keepImport("var _ = " + b.l.hostPkg(path, x) + "." + name)
	return pre, &irExpr{K: "pcall", Pos: p, T: path, Name: name, Args: args}, false
}
