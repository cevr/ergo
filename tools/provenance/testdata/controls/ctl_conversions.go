package check

// Conversions into a node: a pointer retype, and E2b round 4 review M1's
// generic retypes, a conversion to a type parameter whose type set holds a
// node (exact, ~, inferred, union, through an embedded constraint, by
// value), and E2c round 1 review o2's named pointer term.

import "ergo.local/prototype/internal/sketch/syntax"

type retypeRaw syntax.Ident // want `type retypeRaw has the layout of syntax.Ident`

func pointerRetyped() syntax.Node { return (*syntax.Ident)(&retypeRaw{Name: "x"}) } // want `conversion produces a Ident node outside made and clone`

type specRaw syntax.VarSpec // want `type specRaw has the layout of syntax.VarSpec`

func retypeExact[P interface{ *syntax.VarSpec }](r *specRaw) P { return P(r) } // want `conversion to type parameter P, whose type set holds the node VarSpec, outside made and clone` `conversion produces a VarSpec node`

func retypedExact() syntax.Node { return retypeExact[*syntax.VarSpec](&specRaw{}) }

func retypeTilde[P interface{ ~*syntax.VarSpec }](r *specRaw) P { return P(r) } // want `conversion to type parameter P, whose type set holds the node VarSpec`

func retypeInferred[P interface{ *syntax.VarSpec }](r *specRaw) P { return P(r) } // want `conversion to type parameter P, whose type set holds the node VarSpec` `conversion produces a VarSpec node`

func retypedInferred() syntax.Node { return retypeInferred(&specRaw{}) }

func retypeUnion[P interface{ *syntax.VarSpec | *specRaw }](r *specRaw) P { return P(r) } // want `conversion to type parameter P, whose type set holds the node VarSpec`

type nodePointer interface{ *syntax.VarSpec }

func retypeEmbedded[P interface{ nodePointer }](r *specRaw) P { return P(r) } // want `conversion to type parameter P, whose type set holds the node VarSpec`

func retypeValue[E interface{ syntax.VarSpec }](r specRaw) E { return E(r) } // want `conversion to type parameter E, whose type set holds the node VarSpec`

type identRefTerm *syntax.Ident

func retypeNamedPointer[P interface{ identRefTerm }](r *syntax.Ident) P { return P(r) } // want `conversion to type parameter P, whose type set holds the node Ident`
