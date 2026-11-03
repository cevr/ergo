package check

// Package-owned node storage (E2d round 1 review B1): a package variable
// that holds a node by value, directly or in an array or struct, is flagged
// at its declaration, so every address derived from it (&g, &a[0], &s.F) is
// covered, zero or initialized. Another package's such variable is flagged
// at each reference. The reach mutant (stmt.go patch) reports a checker
// diagnostic at one of these addresses.
//
// The class audit's value kinds that copy a node out of storage without an
// Alloc, a literal or a call: a map lookup (Lookup), its comma-ok tuple and
// element (Extract), a field of a struct value (Field), a type assertion to
// a node type (TypeAssert), a channel receive (UnOp), and a Phi of node
// values. Each is a value that holds a node, flagged where it is made.

import "ergo.local/prototype/internal/sketch/syntax"

// The reviewer's zero global, its address a node.
var globalIdent syntax.Ident // want `package variable globalIdent holds a Ident by value: storage outside made and clone whose addresses are nodes in no file`

func globalIdentNode() syntax.Node { return &globalIdent }

// A zero array global, its element's address a node.
var globalIdents [1]syntax.Ident // want `package variable globalIdents holds a Ident by value`

func globalIdentsNode() syntax.Node { return &globalIdents[0] }

// A zero struct global with a node field, the field's address a node.
var globalHolder struct { // want `package variable globalHolder holds a Ident by value`
	N int
	F syntax.Ident
}

func globalHolderNode() syntax.Node { return &globalHolder.F }

// The syntax package's node-holding variable (the test's syntax fixture
// declares E2dZero), referenced from the checker.
func foreignGlobalNode() syntax.Node { return &syntax.E2dZero } // want `syntax.E2dZero holds a Ident by value and is referenced in foreignGlobalNode: a node in no file`

// Lookup: a map of nodes by value yields a copy.
func lookupNode(m map[string]syntax.Ident) syntax.Ident {
	return m["k"] // want `a Ident value the checker makes or copies outside made and clone \(Lookup in lookupNode\)`
}

// Extract: the comma-ok lookup's tuple and its element. An Extract has no
// position; it is reported at its function.
func extractNode(m map[string]syntax.Ident) (syntax.Ident, bool) { // want `a Ident value the checker makes or copies outside made and clone \(Extract in extractNode\)`
	v, ok := m["k"] // want `a Ident value the checker makes or copies outside made and clone \(Lookup in extractNode\)`
	return v, ok
}

// Field: a field of a struct value that is not addressable (a result); the
// result is itself a copy. A field of an addressable struct is a FieldAddr
// and a load, the load flagged as a copy (fieldOfVariable).
func fieldNode(f func() struct{ F syntax.Ident }) syntax.Ident {
	return f().F // want `a Ident value the checker makes or copies outside made and clone \(Call in fieldNode\)` `a Ident value the checker makes or copies outside made and clone \(Field in fieldNode\)`
}

func fieldOfVariable(p *struct{ F syntax.Ident }) syntax.Ident {
	s := *p    // want `allocation of a Ident outside made and clone \(in fieldOfVariable\)` `a Ident value the checker makes or copies outside made and clone \(UnOp in fieldOfVariable\)`
	return s.F // want `a Ident value the checker makes or copies outside made and clone \(UnOp in fieldOfVariable\)`
}

// TypeAssert: an assertion to a node type copies the boxed node.
func assertNode(x any) syntax.Ident {
	return x.(syntax.Ident) // want `a Ident value the checker makes or copies outside made and clone \(TypeAssert in assertNode\)`
}

// Phi: a merge of node values; each operand is a copy where it is made,
// and the Phi (at the variable) is one too.
func phiNode(b bool, x, y *syntax.Ident) syntax.Ident {
	v := *x // want `a Ident value the checker makes or copies outside made and clone \(UnOp in phiNode\)` `a Ident value the checker makes or copies outside made and clone \(Phi in phiNode\)`
	if b {
		v = *y // want `a Ident value the checker makes or copies outside made and clone \(UnOp in phiNode\)`
	}
	return v
}

// UnOp receive: a channel of nodes by value yields a copy.
func receiveNode(ch chan syntax.Ident) syntax.Ident {
	return <-ch // want `a Ident value the checker makes or copies outside made and clone \(UnOp in receiveNode\)`
}
