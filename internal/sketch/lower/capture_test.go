package lower

import (
	"strings"
	"testing"
)

// goBackedLaws is owner q277's rule (types.md, Laws 10): a provider whose
// operations call Go is a law's provider, and the test binary's law case
// builds it, Real by its constructor NewReal, whose row is go.Foreign alone,
// and Twice by a generated value. Real keeps the law; Twice is the planted
// bug, which Go's strings.Repeat makes, so its run fails at a seed.
const goBackedLaws = `import go strings

type Upper {
	Up(s string) string,

	law Idem(s string) { Up(Up(s)) == Up(s) },
}

type Real { tag int } implements Upper

func NewReal() Real needs go.Foreign { Real{tag: len(strings.ToUpper("x"))} }

func Up(r Real, s string) string needs go.Foreign { strings.ToUpper(s) }

type Twice {} implements Upper

func Up(t Twice, s string) string needs go.Foreign { strings.Repeat(s, 2) }

func main() {}
`

// TestGoBackedLawsRunNatively runs goBackedLaws' test binary: both
// bindings run natively, with the ordinary ok and FAIL lines, the failure
// seeded (its seed is the replay), and neither prints skip, so ergo test
// never leaves a native law unrun (the invariant LAW-UNFED counts: after
// ergo test a law is ok or refuted, never native). It builds Go because
// the case calls Go, which the check-time evaluator never runs.
// Mutants: g17-native-skipped (the harness skips a Native binding),
// g17-ctor-dropped (the case builds Real by a generated value, not
// NewReal, so the tag NewReal sets is unread: caught by the Ctor check).
func TestGoBackedLawsRunNatively(t *testing.T) {
	needsBuild(t)
	p := lowerTestBuild(t, "gobacked", goBackedLaws)
	exes := buildAll(t, []*lowered{p}, true)
	if exes == nil {
		return
	}
	stdout, stderr, code := runExeArgs(t, exes["gobacked"], "--seed", "7")
	got := verdicts(stdout)
	if code != 1 || len(got) != 3 || got[0] != "ok law Upper.Idem for Real (100 cases)" ||
		!strings.HasPrefix(got[1], "FAIL law Upper.Idem for Twice (case ") || !strings.Contains(got[1], "seed 7") ||
		got[2] != "FAIL (1 of 2 tests)" {
		t.Fatalf("exit %d, stdout:\n%s\nstderr:\n%s", code, stdout, firstLines(stderr, 8))
	}
	for _, run := range p.pkg.LawRuns {
		for _, b := range run.Bindings {
			for _, lp := range b {
				if !lp.Native || lp.Unrun != "" || (lp.Type.Obj.Name == "Real") != (lp.Ctor != nil) {
					t.Errorf("binding %s: native %t, unrun %q, ctor %v", lp.Type.Obj.Name, lp.Native, lp.Unrun, lp.Ctor)
				}
			}
		}
	}
}

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
