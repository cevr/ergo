package lower

import (
	"go/types"
)

// Round 6 (196b): the checker hands out its lists in canonical order
// (check/canon.go), so a reader of a raw list by any route gets a function
// of the program. The guards of this package prove three narrower facts
// about the lowering's own code (canon.go, "What the tests of this package
// prove"): no direct read of a list field outside canon.go and Validate
// (TestLoweringReadsCanonicalLists), no Unit value outside the entry
// functions (TestLoweringUnitStaysAtEntries), and no import of reflect or
// unsafe (TestLoweringImportsNoReflection).

// mentions reports whether t is u or is built from u.
func mentions(t types.Type, u types.Type, seen map[types.Type]bool) bool {
	if t == nil || seen[t] {
		return false
	}
	seen[t] = true
	if types.Identical(t, u) {
		return true
	}
	switch t := t.(type) {
	case *types.Pointer:
		return mentions(t.Elem(), u, seen)
	case *types.Slice:
		return mentions(t.Elem(), u, seen)
	case *types.Array:
		return mentions(t.Elem(), u, seen)
	case *types.Map:
		return mentions(t.Key(), u, seen) || mentions(t.Elem(), u, seen)
	case *types.Chan:
		return mentions(t.Elem(), u, seen)
	case *types.Tuple:
		for i := 0; i < t.Len(); i++ {
			if mentions(t.At(i).Type(), u, seen) {
				return true
			}
		}
	case *types.Signature:
		return mentions(t.Params(), u, seen) || mentions(t.Results(), u, seen)
	case *types.Struct:
		for i := 0; i < t.NumFields(); i++ {
			if mentions(t.Field(i).Type(), u, seen) {
				return true
			}
		}
	}
	return false
}
