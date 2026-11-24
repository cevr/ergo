package main

import (
	"crypto/sha256"
	"fmt"
	"maps"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

const legacyName = "Unreachable"

type siteID struct{ file, anchor string }

type site struct {
	id   siteID
	line int
}

func boundSites(file, src string, f *syntax.File, pkg *check.Package) []site {
	return callSites(file, src, f, func(call *syntax.CallExpr) bool {
		info := pkg.Info.Calls[call]
		return check.IsUnreachable(info) && info.Func.Pkg.Prelude()
	})
}

func provisionalSites(file, src string, f *syntax.File) []site {
	return callSites(file, src, f, func(call *syntax.CallExpr) bool {
		return bareLegacyCall(call.Fun)
	})
}

func bareLegacyCall(fun syntax.Expr) bool {
	switch f := fun.(type) {
	case *syntax.Ident:
		return f.Name == legacyName
	case *syntax.ParenExpr:
		return bareLegacyCall(f.X)
	case *syntax.IndexExpr:
		return bareLegacyCall(f.X)
	default:
		return false
	}
}

func callSites(file, src string, f *syntax.File, take func(*syntax.CallExpr) bool) []site {
	var out []site
	seen := map[syntax.Pos]bool{} // a derived declaration can share the written call
	var visit func(syntax.Node, string)
	visit = func(n syntax.Node, prefix string) {
		if part := localAnchor(src, n); part != "" {
			prefix += part + "/"
		}
		if call, ok := n.(*syntax.CallExpr); ok && !seen[call.Pos()] && take(call) {
			seen[call.Pos()] = true
			line := 1 + strings.Count(src[:call.Pos()], "\n")
			out = append(out, site{siteID{file, prefix + fingerprint(src, call.Pos(), call.End())}, line})
		}
		first := true
		syntax.Inspect(n, func(child syntax.Node) bool {
			if first {
				first = false
				return true
			}
			p := prefix
			if branch, ok := n.(*syntax.IfStmt); ok {
				switch child {
				case branch.Body:
					p += "then/"
				case branch.Else:
					p += "else/"
				}
			}
			visit(child, p)
			return false
		})
	}
	visit(f, "")
	return out
}

// Named declarations and local control heads distinguish otherwise identical
// calls. Neither line numbers nor ordinals in a body form part of an ID.
func localAnchor(src string, n syntax.Node) string {
	switch n := n.(type) {
	case *syntax.FuncDecl:
		if n.Derived == nil {
			name := "func " + n.Name.Name
			if n.Recv != nil {
				name += " " + fingerprint(src, n.Recv.Pos(), n.Recv.End())
			}
			return name
		}
	case *syntax.TypeDecl:
		return "type " + n.Name.Name
	case *syntax.ContextDecl:
		return "context " + n.Name.Name
	case *syntax.TestDecl:
		return "test " + n.Name.Name
	case *syntax.LawDecl:
		if n.Derived == nil && n.Pair == nil {
			return "law " + n.Name.Name
		}
	case *syntax.VarSpec:
		if n.Pattern != nil {
			return "let " + fingerprint(src, n.Pattern.Pos(), n.Pattern.End())
		}
		names := make([]string, len(n.Names))
		for i, name := range n.Names {
			names[i] = name.Name
		}
		return "let " + strings.Join(names, ",")
	case *syntax.CaseClause:
		return "case " + fingerprint(src, n.Pos(), n.Colon)
	case *syntax.CatchArm:
		return "catch arm " + fingerprint(src, n.Pos(), n.Colon)
	case *syntax.CatchExpr:
		return "catch " + fingerprint(src, n.X.Pos(), n.X.End())
	case *syntax.SwitchStmt:
		return "match " + fingerprint(src, n.Pos(), n.Lbrace)
	case *syntax.TypeSwitchStmt:
		return "match " + fingerprint(src, n.Pos(), n.Lbrace)
	case *syntax.IfStmt:
		return "if " + fingerprint(src, n.Cond.Pos(), n.Cond.End())
	case *syntax.ReturnStmt:
		return "return"
	}
	return ""
}

func fingerprint(src string, start, end syntax.Pos) string {
	if start.IsValid() && end.IsValid() && start <= end && int(end) <= len(src) {
		return fmt.Sprintf("%x", sha256.Sum256([]byte(syntax.TokenText(src[start:end]))))
	}
	return ""
}

func newSites(current []site, baseline map[siteID]int) []site {
	left := maps.Clone(baseline)
	var excess []site
	for _, s := range current {
		if left[s.id] > 0 {
			left[s.id]--
		} else {
			excess = append(excess, s)
		}
	}
	return excess
}
