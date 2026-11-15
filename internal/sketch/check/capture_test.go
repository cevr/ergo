package check

import (
	"strconv"
	"strings"
	"testing"
)

// TestProviderBuildHoldsGoForeign is README Gap 17's captured form for
// go.Foreign (STD-NOPRIV NP4e; types.md, Capabilities 4): a provider whose
// operation calls Go converts to the operation type where it is built, and
// that site is charged go.Foreign; a constraint is no build and captures
// nothing. TestOperationAdapterCallsGo (lower) lowers the admitted program.
// Mutants: capture-without-charge (the build site is not charged),
// capture-in-constraint (a constraint admits the provider).
func TestProviderBuildHoldsGoForeign(t *testing.T) {
	const decls = `import go os

type Pid { Get() int }

type Real {}

func Get(r Real) int needs go.Foreign { os.Getpid() }

func show(p Pid) int { p.Get() }

func generic[P Pid](p P) int { p.Get() }
`
	for _, c := range []struct{ main, want string }{
		{"func main() needs Console | go.Foreign {\n\tConsole.Println(\"\\(show(Real) + with context { Pid: Real } { Pid.Get() })\")\n}\n", ""},
		{"func main() needs Console {\n\tConsole.Println(\"\\(show(Real))\")\n}\n",
			"S102 14:26 building Real as Pid, whose operations call Go, requires go.Foreign, which is not in the capability row of function main and no with provides it"},
		{"func main() needs Console | go.Foreign {\n\tConsole.Println(\"\\(generic(Real))\")\n}\n",
			"S108 14:21 Real does not satisfy the constraint Pid of P: Real.Get requires {go.Foreign}, which the interface's method does not"},
	} {
		src := decls + "\n" + c.main
		_, diags := CheckSource(src)
		var got []string
		for _, d := range diags {
			got = append(got, d.Code+" "+strconv.Itoa(d.Line)+":"+strconv.Itoa(d.Column)+" "+d.Message)
		}
		text := strings.Join(got, "\n")
		if c.want == "" && text != "" || !strings.Contains(text, c.want) || c.want != "" && len(got) != 1 {
			t.Errorf("%s\ngot:\n%s\nwant %q", src, text, c.want)
		}
	}
}
