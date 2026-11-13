package lower

import "testing"

// TestOperationAdapterCallsGo lowers README Gap 17's repro (STD-NOPRIV NP4e):
// a provider whose operation needs go.Foreign becomes an operation value and
// a context entry, and the adapter calls it with the converting frame's
// environment, which holds nothing for go.Foreign (module.go). The checker's
// side is check TestProviderBuildHoldsGoForeign. Mutant:
// adapter-ice-on-foreign (the adapter refuses any table function whose
// capability row is not empty, as before NP4e).
func TestOperationAdapterCallsGo(t *testing.T) {
	const src = `import go os

type Pid { Get() int }

type Real {} implements Pid

func Get(r Real) int needs go.Foreign { os.Getpid() }

func show(p Pid) int { p.Get() }

func main() needs Console | go.Foreign {
	Console.Println("\(show(Real) + with context { Pid: Real } { Pid.Get() })")
}
`
	p := lowerSource("gap17", src, true)
	if p.err != nil || len(p.diags) != 0 || len(p.refused) != 0 || p.panicked != nil {
		t.Fatalf("err=%v diagnostics=%v refusals=%v panic=%v\n%s", p.err, p.diags, p.refused, p.panicked, src)
	}
	typecheckGo(t, []*lowered{p})
}
