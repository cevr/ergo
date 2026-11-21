package bodyedit

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
)

// BodyLines are the old lines, widened by one on each side, that the
// edit from old to now touches, when every hunk lies inside one function
// body in both versions; else the reason it does not.
func BodyLines(old, now string) ([][2]int, string) {
	oldSet, set := token.NewFileSet(), token.NewFileSet()
	oldFile, err := parser.ParseFile(oldSet, "", old, parser.SkipObjectResolution)
	if err != nil {
		return nil, "the base source does not parse"
	}
	nowFile, err := parser.ParseFile(set, "", now, parser.SkipObjectResolution)
	if err != nil {
		return nil, "it does not parse"
	}
	var spans [][2]int
	for _, h := range lineHunks(old, now) {
		before := enclosingBody(oldSet, oldFile, h.OldLo, h.OldHi)
		after := enclosingBody(set, nowFile, h.NewLo, h.NewHi)
		if before == "" || before != after {
			return nil, fmt.Sprintf("lines %d-%d change outside a function body", h.OldLo, h.OldHi)
		}
		spans = append(spans, [2]int{h.OldLo - 1, h.OldHi + 1})
	}
	return spans, ""
}

// enclosingBody names the function whose body strictly holds lines lo..hi
// (an insertion before line lo when hi < lo), or "".
func enclosingBody(set *token.FileSet, file *ast.File, lo, hi int) string {
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		open, close := set.Position(fn.Body.Lbrace).Line, set.Position(fn.Body.Rbrace).Line
		inside := open < lo && hi < close
		if hi < lo {
			inside = open <= lo-1 && lo <= close
		}
		if !inside {
			continue
		}
		name := fn.Name.Name
		if fn.Recv != nil && len(fn.Recv.List) > 0 {
			name = receiverName(fn.Recv.List[0].Type) + "." + name
		}
		return name
	}
	return ""
}

func receiverName(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.StarExpr:
		return "*" + receiverName(e.X)
	case *ast.Ident:
		return e.Name
	case *ast.IndexExpr:
		return receiverName(e.X)
	case *ast.IndexListExpr:
		return receiverName(e.X)
	}
	return fmt.Sprintf("%T", expr)
}
