package check

// Node types the checker declares or spells: local node types, an alias of
// an unnamed node type, an unnamed node copied by value, and the layout ban
// (E2c): a type with a node's underlying type, named, unnamed, tagged, an
// instance, and local to a function.

import "ergo.local/prototype/internal/sketch/syntax"

type spanAt struct{ p, e syntax.Pos } // want `local node type spanAt is outside the parser's trees`

func (s spanAt) Pos() syntax.Pos { return s.p } // want `a spanAt value the checker copies outside made and clone \(parameter s of \(spanAt\).Pos\)` `allocation of a spanAt outside made and clone \(in \(spanAt\).Pos\)`
func (s spanAt) End() syntax.Pos { return s.e } // want `a spanAt value the checker copies` `allocation of a spanAt`

type spanAtRef struct{ p, e syntax.Pos } // want `local node type spanAtRef is outside the parser's trees`

func (s *spanAtRef) Pos() syntax.Pos { return s.p }
func (s *spanAtRef) End() syntax.Pos { return s.e }

type nodeAlias = struct{ *syntax.Ident } // want `alias nodeAlias declares the unnamed node type struct\{\*Ident\}`

func unnamedCopied(p struct{ *syntax.Ident }) syntax.Node { return p } // want `a struct\{\*Ident\} value the checker copies outside made and clone \(parameter p of unnamedCopied\)`

type layoutRaw syntax.Ident // want `type layoutRaw has the layout of syntax.Ident; a conversion from it makes a node outside made and clone`

func layoutHeld(p **struct { // want `type struct\{NamePos syntax.Pos; Name string\} has the layout of syntax.Ident`
	NamePos syntax.Pos
	Name    string
}) {
}

type layoutTagged struct { // want `type layoutTagged has the layout of syntax.Ident`
	NamePos syntax.Pos "pos"
	Name    string
}

type layoutOf[T any] struct { // want `type layoutOf\[string\] has the layout of syntax.Ident`
	NamePos syntax.Pos
	Name    T
}

var _ *layoutOf[string]

func layoutLocal() int {
	type raw syntax.Ident // want `type raw has the layout of syntax.Ident`
	return len(raw{}.Name)
}
