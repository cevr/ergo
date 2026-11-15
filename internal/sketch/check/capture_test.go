package check

import (
	"slices"
	"strconv"
	"strings"
	"testing"
)

// TestProviderBuildHoldsGoForeign is README Gap 17's captured form for
// go.Foreign (STD-NOPRIV NP4e; types.md, Capabilities 4): a provider whose
// operation calls Go converts to the operation type where it is built, and
// that site is charged go.Foreign; a constraint is no build and captures
// nothing. TestOperationAdapterCallsGo (lower) lowers the admitted program.
// A law of the operation type does not run for the provider, since a law
// case never holds go.Foreign (rows.md, Entry 4): its run names the reason
// (GAP17). Mutants: capture-without-charge (the build site is not
// charged), capture-in-constraint (a constraint admits the provider),
// gap17-law-silent (the law's run drops the provider with no reason).
func TestProviderBuildHoldsGoForeign(t *testing.T) {
	const decls = `import go os

type Pid {
	Get() int,

	law Stable() { Get() == Get() },
}

type Real {} implements Pid

func Get(r Real) int needs go.Foreign { os.Getpid() }

func show(p Pid) int { p.Get() }

func generic[P Pid](p P) int { p.Get() }
`
	for _, c := range []struct{ main, want string }{
		{"func main() needs Console | go.Foreign {\n\tConsole.Println(\"\\(show(Real) + with context { Pid: Real } { Pid.Get() })\")\n}\n", ""},
		{"func main() needs Console {\n\tConsole.Println(\"\\(show(Real))\")\n}\n",
			"S102 18:26 building Real as Pid, whose operations call Go, requires go.Foreign, which is not in the capability row of function main and no with provides it"},
		{"func main() needs Console | go.Foreign {\n\tConsole.Println(\"\\(generic(Real))\")\n}\n",
			"S108 18:21 Real does not satisfy the constraint Pid of P: Real.Get requires {go.Foreign}, which the interface's method does not"},
	} {
		src := decls + "\n" + c.main
		pkg, diags := CheckSource(src)
		var got []string
		for _, d := range diags {
			got = append(got, d.Code+" "+strconv.Itoa(d.Line)+":"+strconv.Itoa(d.Column)+" "+d.Message)
		}
		text := strings.Join(got, "\n")
		if c.want == "" {
			var reasons []string
			for _, r := range pkg.LawRuns {
				for _, b := range r.Bindings {
					for _, lp := range b {
						reasons = append(reasons, r.Law.Name+" for "+lp.Type.Obj.Name+": "+lp.Unrun)
					}
				}
			}
			if want := "Stable for Real: building Real as Pid calls Go and requires go.Foreign, which only the program entry holds and a law case never does (README Gap 17)"; !slices.Contains(reasons, want) {
				t.Errorf("law runs %q, want %q", reasons, want)
			}
		}
		if c.want == "" && text != "" || !strings.Contains(text, c.want) || c.want != "" && len(got) != 1 {
			t.Errorf("%s\ngot:\n%s\nwant %q", src, text, c.want)
		}
	}
}
