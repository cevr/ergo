package lower

import (
	"go/ast"
)

func astTextType(x ast.Expr) string {
	switch x := x.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.StarExpr:
		return "*" + astTextType(x.X)
	}
	return ""
}
