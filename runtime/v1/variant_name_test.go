package v1

import "testing"

// idle_1 stands for a variant the lowering suffixed (Idle_1 beside the
// root's Idle): it gives its Ergo name, as the lowering writes it
// (lower machine.go, suffixedVariantMethod).
type idle_1 struct{ N int }

func (idle_1) ErgoVariant() string { return "Idle" }

type box_1[P0 any] struct{ V P0 }

func (box_1[P0]) ErgoVariant() string { return "Box" + GoVariantOf[box_1[P0]]()[len("box_1"):] }

type plain struct{}

// TestVariantOfReadsTheErgoName (TRACE-VARIANT-SUFFIX): VariantOf, which a
// generic flat union's ErgoVariant and the reference graph registry name
// variants with, gives the name a variant gives itself, as variant does
// for a value, so a graph's names and a value's agree; else the Go type's
// name. Mutant variantof-go-name (lower's ledger has the lowering's).
func TestVariantOfReadsTheErgoName(t *testing.T) {
	for _, c := range []struct{ got, want string }{
		{VariantOf[idle_1](), "Idle"},
		{VariantOf[box_1[int]](), "Box[int]"},
		{VariantOf[plain](), "plain"},
		{variant(nil, idle_1{}), "Idle"},
		{variant(nil, box_1[string]{}), "Box[string]"},
	} {
		if c.got != c.want {
			t.Errorf("got %q, want %q", c.got, c.want)
		}
	}
}
