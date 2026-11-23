package irspike

import (
	_ "embed"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
)

// TestIRMatchBindingsMatchSource checks that a catch can bind its whole
// failure into a generic slot, and that a match arm shared by different
// patterns keeps the variant and payload of each leaf's binding.
//
// It builds Go because the patched compiler must accept each binding's
// host type, and only execution observes which value reaches the arm.
//
// Mutants ir-tree-caught-binding-unconverted and
// ir-tree-shared-binding-refused skip the conversion to the type
// the checker gave the binding.
func TestIRMatchBindingsMatchSource(t *testing.T) {
	f7Property(t, "TestIRMatchBindingsMatchSource", matchBindingProgram, "observe", "joined")
}

//go:embed testdata/match_binding.ergo
var matchBindingFixture string

func matchBindingProgram(seed uint64) string {
	r := rand.New(rand.NewPCG(seed, seed^0x789ef124b8334759))
	inputs := fmt.Sprintf("exercise(%d, %d, %d)",
		1+r.IntN(100), -1-r.IntN(100), 1+r.IntN(100))
	text := strings.Replace(matchBindingFixture, "exercise(11, -12, 13)", inputs, 1)
	if r.IntN(2) == 0 {
		text = strings.Replace(text, "A{Item: item}, B{Item: item}", "B{Item: item}, A{Item: item}", 1)
	}
	return text
}
