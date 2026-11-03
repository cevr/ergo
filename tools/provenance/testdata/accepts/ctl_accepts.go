package check

// Shapes that make or copy no node and stay clean (E2b round 3 review m1,
// E2c, E2c round 2): pointer and interface type arguments, conversions to
// an interface no node-yielding method implements, an alias of a named node
// type, the syntax package's read-only helpers, a type-parameter conversion
// with no node term, a layout one field off a node's, a named pointer to a
// node type, made and clone called with explicit type arguments and from a
// function literal, an identity pointer conversion (SSA folds it), element
// copies in a generic body, and a range over a map's keys.

import (
	"maps"
	"slices"

	"ergo.local/prototype/internal/sketch/syntax"
)

func acceptIdentity[T any](p T) T { return p }

var acceptNodeIdentity = acceptIdentity[*syntax.Ident]

func acceptHas(xs []syntax.Expr, x syntax.Expr) bool { return slices.Contains(xs, x) }

func acceptKeyClone(m map[*syntax.SelectorExpr]int) map[*syntax.SelectorExpr]int {
	return maps.Clone(m)
}

func acceptWidened(x *syntax.Ident) (syntax.Node, syntax.Expr) { return syntax.Node(x), syntax.Expr(x) }

type acceptIdent = syntax.Ident
type acceptIdentRef = *syntax.Ident

func acceptWalked(n syntax.Node) (k int) {
	syntax.Inspect(n, func(syntax.Node) bool { k++; return !syntax.IsNil(n) })
	return k
}

func acceptWiden[T ~int | ~int64](x int) T { return T(x) }

type acceptNameAt struct {
	NamePos syntax.Pos
	name    string
}

type acceptPosName struct {
	Name    string
	NamePos syntax.Pos
}

type acceptNodeRef *syntax.Ident

func acceptRef(x *syntax.Ident) acceptNodeRef { return acceptNodeRef(x) }

func acceptExplicitCalls(c *checker, p *syntax.Ident) (syntax.Node, syntax.Node) {
	return clone[syntax.Ident, *syntax.Ident](c, p), made[*syntax.Ident](c, &syntax.Ident{}, p)
}

func acceptClosureCalls(c *checker, p *syntax.Ident) func() syntax.Node {
	return func() syntax.Node { return clone(c, p) }
}

func acceptConverted(p *syntax.Ident) syntax.Node { return (*syntax.Ident)(p) }

func acceptSorted[T any](xs, buf []T) {
	for i := range xs {
		buf[i] = xs[i]
	}
}

func acceptKeys(m map[string]int) (n int) {
	for range m {
		n++
	}
	for k := range m {
		n += len(k)
	}
	return n
}
