package lower

import (
	"slices"
	"testing"
)

// TestRowArgOfTheCallerTakesIR (fallback reason "generic call with a row type argument of the caller", std ui.crashOf): a generic function that passes its own
// error row parameter, which comes from std actor's extern go Actor, on to
// actor.ExitOf (std ui's crashOf) takes an IR body. The call's row type
// argument is the caller's Go type parameter, or error where the row
// parameter has none, as the emitter's goArg writes it (rowslot.go).
// Mutant ly-rowarg-caller-refused (ir_pkg.go goArg refuses the caller's
// row parameter again) fails it.
func TestRowArgOfTheCallerTakesIR(t *testing.T) {
	src := `import actor

func failOf[S, E, O, F](a actor.Actor[S, E, Decision[S, O], F]) int {
	match actor.ExitOf(a) {
	case actor.Failed[F]:
		1
	case actor.Defected:
		2
	case _:
		0
	}
}

func main() {
}
`
	_, rep, err := LowerIR(factsUnit(t, src))
	if err != nil {
		t.Fatal(err)
	}
	if n := rep.Fallback["generic call with a row type argument of the caller"]; n > 0 || !slices.Contains(rep.IR, "failOf") {
		t.Fatalf("failOf keeps its source body (row-argument fallbacks %d; fallback %v; IR bodies %v)", n, rep.Fallback, rep.IR)
	}
}
