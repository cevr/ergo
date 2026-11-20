package v1

import "testing"

// idle_1 and box_1 stand for types the lowering suffixed (Idle_1 beside
// the root's Idle); plain for one it did not.
type idle_1 struct{ N int }

type box_1[P0 any] struct{ V P0 }

type plain struct{}

// TestVariantsHaveTheirErgoNames (TRACE-VARIANT-SUFFIX): under the table a
// program registers, VariantOf, which a generic flat union's ErgoVariant
// and the reference graph registry name variants with, and variant, which
// names a value no graph names, spell a suffixed type by its Ergo name, as
// the head and as a type argument of its package; a name the table does
// not hold, or one of another package, keeps its spelling. Mutants
// (testdata/mutants.json): variant-names-go-name, variant-names-head-only.
func TestVariantsHaveTheirErgoNames(t *testing.T) {
	keepPkg, keep := typePkg, typeNames
	t.Cleanup(func() { typePkg, typeNames = keepPkg, keep })
	RegisterTypeNames(Path, "idle_1", "Idle", "box_1", "Box", "N", "M")
	for _, c := range []struct{ got, want string }{
		{VariantOf[idle_1](), "Idle"},
		{VariantOf[box_1[idle_1]](), "Box[" + Path + ".Idle]"},
		{VariantOf[box_1[box_1[idle_1]]](), "Box[" + Path + ".Box[" + Path + ".Idle]]"},
		{VariantOf[box_1[plain]](), "Box[" + Path + ".plain]"},
		{VariantOf[plain](), "plain"},
		{variant(nil, idle_1{}), "Idle"},
		{variant(nil, box_1[idle_1]{}), "Box[" + Path + ".Idle]"},
		// another package's names, whose paths end in this one's or its
		// name's, and a name no package qualifies
		{ErgoTypeName("Box_9[x/main.idle_1, xmain.idle_1, struct { N int }]", "main", typeNames), "Box_9[x/main.idle_1, xmain.idle_1, struct { N int }]"},
		{ErgoTypeName("Box_9[main.idle_1]", "main", typeNames), "Box_9[main.Idle]"},
	} {
		if c.got != c.want {
			t.Errorf("got %q, want %q", c.got, c.want)
		}
	}
}
