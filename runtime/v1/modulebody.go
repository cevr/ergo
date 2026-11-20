package v1

import (
	"bytes"
	"fmt"
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
// PureImports, no package variable, no goroutine, no channel), read on
// the body and on each function of its package it calls, transitively,
// each a plain function with a Go body: a function written in assembly
// has none to read. The rules are read from the source; they are no
// review. An effect-free body is still not pure: only a validated law
// makes it so.
func (b Bodies) Effects(name string) string {
	_, why := b.closure(name)
	return why
}

// closure is the body name and the package functions it reaches, in the
// order they are first named, or why it is not effect-free.
func (b Bodies) closure(name string) ([]reachDecl, string) {
	g, ok := b.decls[name]
	if !ok || g.fn == nil {
		return nil, "it is no function of the package"
	}
	if g.fn.Body == nil {
		return nil, "it has no Go body to read (assembly)"
	}
	body, err := goShape(g.fn.Type, g.q)
	if err != nil {
		return nil, err.Error()
	}
	vars := map[string]bool{}
	for v := range g.index.vars {
		vars[v] = true
	}
	// The body's own rules first, with the package's functions allowed:
	// each is read below.
	own := g
	own.values = vars
	if why := pureRefusal(own, body); why != "" {
		return nil, why
	}
	out := []reachDecl{{node: g.fn, locals: g.locals}}
	seen := map[string]bool{name: true}
	for i := 0; i < len(out); i++ {
		fn := out[i].node.(*ast.FuncDecl)
		var why string
		var visit func(n ast.Node) bool
		visit = func(n ast.Node) bool {
			if why != "" {
				return false
			}
			if s, ok := n.(*ast.SelectorExpr); ok {
				// s.Sel names a field, a method or an imported member.
				ast.Inspect(s.X, visit)
				return false
			}
			id, ok := n.(*ast.Ident)
			if !ok || seen[id.Name] || len(g.index.funcs[id.Name]) == 0 {
				return true
			}
			seen[id.Name] = true
			decls := g.index.funcs[id.Name]
			h, isFn := decls[0].node.(*ast.FuncDecl)
			switch {
			case len(decls) != 1 || !isFn:
				why = fmt.Sprintf("it calls %s, which is declared more than once", id.Name)
			case h.Body == nil:
				why = fmt.Sprintf("it calls %s, which has no Go body to read (assembly)", id.Name)
			case h.Type.TypeParams != nil:
				why = fmt.Sprintf("it calls %s, a generic function", id.Name)
			default:
				hd := goDecl{fn: h, locals: decls[0].locals, values: vars, index: g.index}
				for _, p := range decls[0].locals {
					hd.imports = append(hd.imports, p)
				}
				if w := helperRefusal(hd); w != "" {
					why = fmt.Sprintf("it calls %s, and %s", id.Name, w)
				} else {
					out = append(out, decls[0])
				}
			}
			return why == ""
		}
		ast.Inspect(fn.Body, visit)
		if why != "" {
			return nil, why
		}
	}
	return out, ""
}

// helperRefusal is pureRefusal's import and body rules for a function the
// body calls, whose shape no Ergo signature fixes.
func helperRefusal(g goDecl) string {
	g.fn = &ast.FuncDecl{Name: g.fn.Name, Type: &ast.FuncType{Params: &ast.FieldList{}}, Body: g.fn.Body}
	return pureRefusal(g, Body{})
}

// Source is the Go text of the module body name, standing alone: its
// declaration, the package functions it calls, and the imports of their
// files that they name, for the licence run's driver (LAWFFI LF2m), which
// runs it in a package of its own. ok is false for a body that is not
// effect-free (Effects): only such a body names no package variable.
func (b Bodies) Source(name string) (decl string, imports []string, ok bool) {
	decls, why := b.closure(name)
	if why != "" {
		return "", nil, false
	}
	seen := map[string]bool{}
	var buf bytes.Buffer
	for _, d := range decls {
		fn := *d.node.(*ast.FuncDecl)
		used := map[string]bool{}
		ast.Inspect(&fn, func(n ast.Node) bool {
			if s, isSel := n.(*ast.SelectorExpr); isSel {
				if id, isID := s.X.(*ast.Ident); isID {
					used[id.Name] = true
				}
			}
			return true
		})
		for local, p := range d.locals {
			if !used[local] {
				continue
			}
			spec := strconv.Quote(p)
			if local != importName(p) {
				spec = local + " " + spec
			}
			if !seen[spec] {
				seen[spec] = true
				imports = append(imports, spec)
			}
		}
		fn.Doc = nil
		if buf.Len() > 0 {
			buf.WriteString("\n\n")
		}
		if err := format.Node(&buf, token.NewFileSet(), &fn); err != nil {
			return "", nil, false
		}
	}
	slices.Sort(imports)
	return buf.String(), imports, true
}
