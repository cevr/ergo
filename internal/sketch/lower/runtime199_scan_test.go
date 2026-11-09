package lower

import (
	"go/ast"
	"go/token"
	"go/types"
	"strings"
	"testing"
)

// typedSource is a production Go package of this module, parsed and
// type-checked, so an owner scan resolves names through go/types: an alias, a
// method value, a local copy or a helper cannot hide a use the way a spelling
// can.
type typedSource struct {
	fset  *token.FileSet
	files []*ast.File
	info  *types.Info
	pkg   *types.Package
}

// owners visits each top-level declaration with its qualified owner name:
// "Recv.Method" for a method, the name for a function, "file:package"
// otherwise.
func (s *typedSource) owners(visit func(owner string, decl ast.Decl)) {
	for _, f := range s.files {
		for _, decl := range f.Decls {
			visit(declOwner(s.fset, decl), decl)
		}
	}
}

func declOwner(fset *token.FileSet, decl ast.Decl) string {
	fn, ok := decl.(*ast.FuncDecl)
	if !ok {
		name := fset.Position(decl.Pos()).Filename
		return name[strings.LastIndex(name, "/")+1:] + ":package"
	}
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return fn.Name.Name
	}
	recv := fn.Recv.List[0].Type
	if star, ok := recv.(*ast.StarExpr); ok {
		recv = star.X
	}
	if idx, ok := recv.(*ast.IndexExpr); ok {
		recv = idx.X
	}
	return types.ExprString(recv) + "." + fn.Name.Name
}

// method resolves the named method of a package type.
func (s *typedSource) method(t *testing.T, typ, name string) types.Object {
	t.Helper()
	tn, ok := s.pkg.Scope().Lookup(typ).(*types.TypeName)
	if !ok {
		t.Fatalf("%s: no type %s", s.pkg.Path(), typ)
	}
	obj, _, _ := types.LookupFieldOrMethod(types.NewPointer(tn.Type()), true, s.pkg, name)
	if obj == nil {
		t.Fatalf("%s.%s: no method %s", s.pkg.Path(), typ, name)
	}
	return obj
}

// users is the set of owners whose declarations refer to obj (a call, a
// method value or any other use).
func (s *typedSource) users(obj types.Object) map[string]bool {
	out := map[string]bool{}
	s.owners(func(owner string, decl ast.Decl) {
		ast.Inspect(decl, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok && s.info.Uses[id] == obj {
				out[owner] = true
			}
			return true
		})
	})
	return out
}
