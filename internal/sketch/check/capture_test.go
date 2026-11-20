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
// A law of the operation type runs natively for the provider: the test
// binary's law case holds go.Foreign (types.md, Laws 10; rows.md, Entry 4;
// owner q277), so the binding is Native with no reason, and nothing else
// gains the authority: a test body, a library function and a law's body
// that build the provider or call Go are still S102.
// Mutants: capture-without-charge (the build site is not charged),
// capture-in-constraint (a constraint admits the provider),
// g17-native-dropped (the binding is not marked Native),
// g17-test-body-granted (a test body holds go.Foreign),
// g17-library-granted (a non-entry function builds the provider without
// go.Foreign), g17-law-body-granted (a law's body holds go.Foreign).
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
			"S108 18:21 Real does not satisfy the constraint Pid of generic's P: Real.Get requires {go.Foreign}, which the interface's method does not"},
		{"func build() int { show(Real) }\n\nfunc main() {}\n",
			"S102 17:25 building Real as Pid, whose operations call Go, requires go.Foreign, which is not in the capability row of function build"},
		{"test Builds {\n\tassert show(Real) > 0\n}\n\nfunc main() {}\n",
			"S102 18:14 building Real as Pid, whose operations call Go, requires go.Foreign, which is not in the capability row of test Builds"},
	} {
		src := decls + "\n" + c.main
		pkg, diags := CheckSource(src)
		var got []string
		for _, d := range diags {
			got = append(got, d.Code+" "+strconv.Itoa(d.Line)+":"+strconv.Itoa(d.Column)+" "+d.Message)
		}
		text := strings.Join(got, "\n")
		if c.want == "" {
			var runs []string
			for _, r := range pkg.LawRuns {
				for _, b := range r.Bindings {
					for _, lp := range b {
						runs = append(runs, r.Law.Name+" for "+lp.Type.Obj.Name+": native "+strconv.FormatBool(lp.Native)+", unrun "+strconv.Quote(lp.Unrun))
					}
				}
			}
			if want := `Stable for Real: native true, unrun ""`; !slices.Contains(runs, want) {
				t.Errorf("law runs %q, want %q", runs, want)
			}
		}
		if c.want == "" && text != "" || !strings.Contains(text, c.want) || c.want != "" && len(got) != 1 {
			t.Errorf("%s\ngot:\n%s\nwant %q", src, text, c.want)
		}
	}
	// A law's body keeps Laws 7's row, the slot and nothing else, so its
	// call of Go is not the case's build and holds no go.Foreign.
	const lawBody = `import go os

type Pid {
	Get() int,

	law Mine() { Get() == os.Getpid() },
}

func main() {}
`
	_, diags := CheckSource(lawBody)
	var got []string
	for _, d := range diags {
		got = append(got, d.Code+" "+strconv.Itoa(d.Line)+":"+strconv.Itoa(d.Column)+" "+d.Message)
	}
	if text := strings.Join(got, "\n"); len(got) != 1 || !strings.HasPrefix(text, "S102 6:") || !strings.Contains(text, "go.Foreign") {
		t.Errorf("%s\ngot:\n%s\nwant one S102 at 6: naming go.Foreign", lawBody, text)
	}
}
