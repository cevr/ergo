package check

// Generic bodies: a copy, a pointer copy and a literal of a type parameter
// with no instance in sight, a function value of a generic instance with a
// node type argument, and E2c round 1 review m2: a type with a node's layout
// that exists only by substitution in a generic body, which each instance
// the package builds shows.

import "ergo.local/prototype/internal/sketch/syntax"

func dupNode[T any](p *T) *T {
	v := *p // want `construction or copy of type parameter T outside made and clone`
	return &v
}

func copyPointer[T any](p **T) *T { return *p } // want `construction or copy of type parameter T`

func literal[T ~struct{ X int }]() *T { return &T{X: 1} } // want `construction or copy of type parameter T`

// A zero node value, boxed: SSA gives it as a constant operand, in a
// generic instance as in plain code.
func zeroOf[T any]() any { var z T; return z } // want `a zero Ident value the checker makes outside made and clone \(in zeroOf\)`

func zeroIdent() any { return zeroOf[syntax.Ident]() } // want `generic instantiation zeroOf with node argument Ident`

func zeroBoxed() any { return syntax.Ident{} } // want `a zero Ident value the checker makes outside made and clone \(in zeroBoxed\)`

func identity[T any](p T) T { return p } // want `a Ident value the checker copies outside made and clone \(parameter p of identity\)`

var nodeIdentity = identity[syntax.Ident] // want `generic instantiation identity with node argument Ident`

type substWrap[T any] struct {
	NamePos T
	Name    string
}

func substInner[T any](p T) any { return &substWrap[T]{NamePos: p} } // want `type substWrap\[syntax.Pos\] has the layout of syntax.Ident`

func substOuter(id *syntax.Ident) any { return substInner(id.NamePos) }
