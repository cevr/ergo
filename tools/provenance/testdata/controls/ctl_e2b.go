package check

// E2b controls: an eighth allocation, a shallow copy that escapes, a new, a
// literal nested in a made one, and a node value read out of a struct.

import "ergo.local/prototype/internal/sketch/syntax"

func e2bControlAlloc() syntax.Node { return &syntax.Ident{Name: "x"} } // want `allocation of a Ident outside made and clone \(in e2bControlAlloc\)`

func e2bControlCopy(s *syntax.VarSpec) syntax.Node {
	v := *s // want `allocation of a VarSpec outside made and clone` `a VarSpec value the checker makes or copies outside made and clone \(UnOp in e2bControlCopy\)`
	return &v
}

func e2bControlNew() syntax.Node { return new(syntax.BasicLit) } // want `allocation of a BasicLit outside made and clone`

func e2bControlNested(c *checker, vs *syntax.VariantSpec) syntax.Node {
	return made(c, &syntax.TypeDecl{Spec: &syntax.StructType{}}, vs) // want `construction site e2bControlNested makes a TypeDecl and is not in the manifest` `allocation of a StructType outside made and clone`
}

func e2bControlValue(f syntax.Field) syntax.Node { return f.Type } // want `a Field value the checker copies outside made and clone \(parameter f of e2bControlValue\)` `allocation of a Field`
