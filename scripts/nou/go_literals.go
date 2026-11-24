package main

import (
	"go/ast"
	"go/token"
	"strconv"
	"strings"
)

// goText maps offsets in decoded fixture text back to their literal in
// the Go source. Newlines escaped inside a quoted literal do not move its
// source line. A raw literal has the same newlines as the Ergo text.
type goText struct {
	text  string
	parts []goPart
}

type goPart struct {
	start, line int
	raw         bool
}

func (s goText) lineAt(offset int) int {
	for i := len(s.parts) - 1; i >= 0; i-- {
		p := s.parts[i]
		if offset >= p.start {
			if p.raw {
				return p.line + strings.Count(s.text[p.start:offset], "\n")
			}
			return p.line
		}
	}
	return s.parts[0].line
}

// literalText folds only Go's written literal concatenation. It does not run
// property generators, execute templates, or resolve interpolated Go values.
func literalText(fset *token.FileSet, n ast.Node, s *strings.Builder, parts *[]goPart) bool {
	switch n := n.(type) {
	case *ast.BasicLit:
		if n.Kind != token.STRING {
			return false
		}
		text, err := strconv.Unquote(n.Value)
		if err != nil {
			return false
		}
		*parts = append(*parts, goPart{s.Len(), fset.PositionFor(n.Pos(), false).Line, strings.HasPrefix(n.Value, "`")})
		s.WriteString(text)
		return true
	case *ast.BinaryExpr:
		return n.Op == token.ADD && literalText(fset, n.X, s, parts) && literalText(fset, n.Y, s, parts)
	case *ast.ParenExpr:
		return literalText(fset, n.X, s, parts)
	}
	return false
}

func goSelector(expr ast.Expr) string {
	switch expr := expr.(type) {
	case *ast.Ident:
		return expr.Name
	case *ast.SelectorExpr:
		return goSelector(expr.X) + "." + expr.Sel.Name
	case *ast.IndexExpr:
		return goSelector(expr.X)
	case *ast.IndexListExpr:
		return goSelector(expr.X)
	}
	return ""
}

func goOwner(stack []ast.Node, text ast.Node) string {
	var parts []string
	for _, n := range stack {
		switch n := n.(type) {
		case *ast.FuncDecl:
			name := n.Name.Name
			if n.Recv != nil {
				typ := n.Recv.List[0].Type
				if p, ok := typ.(*ast.StarExpr); ok {
					typ = p.X
				}
				name = goSelector(typ) + "." + name
			}
			parts = append(parts, "go func "+name)
		case *ast.ValueSpec:
			for _, name := range n.Names {
				parts = append(parts, "go value "+name.Name)
			}
		case *ast.KeyValueExpr:
			if n.Value == text || n.Value.Pos() <= text.Pos() && text.End() <= n.Value.End() {
				if key := goSelector(n.Key); key != "" {
					parts = append(parts, "go field "+key)
				} else if key, ok := n.Key.(*ast.BasicLit); ok && key.Kind == token.STRING {
					if decoded, err := strconv.Unquote(key.Value); err == nil {
						parts = append(parts, "go key "+strconv.Quote(decoded))
					}
				}
			}
		}
	}
	return strings.Join(parts, "/")
}

// A Go diagnostic or search needle is not source sent to the Ergo compiler.
// Check the argument, not the file or enclosing function.
func goHostText(stack []ast.Node, n ast.Node) bool {
	for len(stack) > 0 {
		parent := stack[len(stack)-1]
		switch p := parent.(type) {
		case *ast.ParenExpr:
		case *ast.BinaryExpr:
			if p.Op != token.ADD {
				return false
			}
		default:
			if call, ok := p.(*ast.CallExpr); ok {
				return goDiagnosticArg(call, n)
			}
			return goExpectedText(stack, n)
		}
		n, stack = parent, stack[:len(stack)-1]
	}
	return false
}

func goDiagnosticArg(call *ast.CallExpr, n ast.Node) bool {
	isArg := func(i int) bool { return len(call.Args) > i && call.Args[i] == n }
	switch goSelector(call.Fun) {
	case "strings.Contains", "strings.HasPrefix", "strings.HasSuffix", "strings.Index", "strings.Count":
		return isArg(1)
	case "c.errorf":
		return isArg(2)
	case "fmt.Errorf", "t.Fatalf", "t.Errorf", "t.Logf":
		return isArg(0)
	}
	return false
}

// Go fixtures often put name, src and want in an anonymous row type. Use
// that written field order rather than guessing that the diagnostic string
// is a candidate merely because it quotes a call.
func goExpectedText(stack []ast.Node, n ast.Node) bool {
	if len(stack) < 2 {
		return false
	}
	row, ok := stack[len(stack)-1].(*ast.CompositeLit)
	if !ok {
		return false
	}
	rows, ok := stack[len(stack)-2].(*ast.CompositeLit)
	if !ok {
		return false
	}
	array, ok := rows.Type.(*ast.ArrayType)
	if !ok {
		return false
	}
	fields, ok := array.Elt.(*ast.StructType)
	if !ok {
		return false
	}
	i, source, expected := 0, false, false
	for _, field := range fields.Fields.List {
		for _, name := range field.Names {
			source = source || name.Name == "src"
			expected = expected || name.Name == "want" && i < len(row.Elts) && row.Elts[i] == n
			i++
		}
	}
	return source && expected
}

func goFormat(stack []ast.Node, n ast.Node) bool {
	if len(stack) == 0 {
		return false
	}
	call, ok := stack[len(stack)-1].(*ast.CallExpr)
	if !ok {
		return false
	}
	switch goSelector(call.Fun) {
	case "fmt.Fprintf":
		return len(call.Args) > 1 && call.Args[1] == n
	case "fmt.Sprintf":
		return len(call.Args) > 0 && call.Args[0] == n
	}
	return false
}

// substituteFormat leaves source offsets alone and chooses syntactic
// stand-ins for common format holes. The real generator's values are not
// evaluated; an otherwise incomplete template still needs its exact
// fragment entry.
func substituteFormat(s string) string {
	return strings.NewReplacer("%%", " %", "%q", `""`, "%s", "__", "%d", "10", "%x", "10", "%v", "__").Replace(s)
}
