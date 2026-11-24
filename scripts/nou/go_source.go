package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
)

// embeddedSites extracts source strings without executing Go. Fold literal
// concatenation once, at its largest expression, rather than counting its
// children again. Host implementation code is never parsed as Ergo.
func embeddedSites(file, text string) ([]site, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, text, parser.SkipObjectResolution)
	if err != nil && strings.Contains(text, legacyName) {
		return nil, fmt.Errorf("%s: cannot extract legacy fixtures from malformed Go: %w", file, err)
	}
	if f == nil {
		return nil, nil
	}
	var all []site
	var failed error
	ast.PreorderStack(f, nil, func(n ast.Node, stack []ast.Node) bool {
		if failed != nil {
			return false
		}
		switch n.(type) {
		case *ast.BasicLit, *ast.BinaryExpr, *ast.ParenExpr:
		default:
			return true
		}
		var b strings.Builder
		var parts []goPart
		if !literalText(fset, n, &b, &parts) {
			return true
		}
		src := b.String()
		if !strings.Contains(src, legacyName) || goHostText(stack, n) {
			return false
		}
		sites, err := ergoLiteralSites(file, goOwner(stack, n), goText{src, parts}, goFormat(stack, n))
		if err != nil {
			failed = fmt.Errorf("%s:%d: %w", file, parts[0].line, err)
			return false
		}
		all = append(all, sites...)
		return false
	})
	return all, failed
}
