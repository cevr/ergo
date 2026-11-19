package v1

import (
	"bytes"
	"go/ast"
	"go/format"
	"go/token"
	"slices"
	"strconv"
)

// Effects is why the module body name may not be pure, or "" when it is
// effect-free (LAWFFI LF2m; docs/spec/interop.md, Trusted Go 3): the
// mechanical rules of a pure body (pureRefusal: a plain function of no
// hidden argument and no function parameter, a file that imports only
// PureImports, no package state, no goroutine, no channel), and a Go
// body to read them on: a function written in assembly has none. The
// rules are read from the source; they are no review. An effect-free
// body is still not pure: only a validated law makes it so.
func (b Bodies) Effects(name string) string {
	g, ok := b.decls[name]
	if !ok || g.fn == nil {
		return "it is no function of the package"
	}
	if g.fn.Body == nil {
		return "it has no Go body to read (assembly)"
	}
	body, err := goShape(g.fn.Type, g.q)
	if err != nil {
		return err.Error()
	}
	return pureRefusal(g, body)
}

// Source is the Go text of the module body name, standing alone: its
// declaration and the imports of its file that the declaration names,
// for the licence run's driver (LAWFFI LF2m), which runs it in a package
// of its own. ok is false for a body that is not effect-free (Effects):
// only such a body names nothing else of its package.
func (b Bodies) Source(name string) (decl string, imports []string, ok bool) {
	if b.Effects(name) != "" {
		return "", nil, false
	}
	g := b.decls[name]
	used := map[string]bool{}
	ast.Inspect(g.fn, func(n ast.Node) bool {
		if s, isSel := n.(*ast.SelectorExpr); isSel {
			if id, isID := s.X.(*ast.Ident); isID {
				used[id.Name] = true
			}
		}
		return true
	})
	for local, p := range g.locals {
		if !used[local] {
			continue
		}
		spec := strconv.Quote(p)
		if local != importName(p) {
			spec = local + " " + spec
		}
		imports = append(imports, spec)
	}
	slices.Sort(imports)
	fn := *g.fn
	fn.Doc = nil
	var buf bytes.Buffer
	if err := format.Node(&buf, token.NewFileSet(), &fn); err != nil {
		return "", nil, false
	}
	return buf.String(), imports, true
}
