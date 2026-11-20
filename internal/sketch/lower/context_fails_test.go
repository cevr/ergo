package lower

import (
	"strings"
	"testing"
)

// contextFailsSource has two named contexts whose header writes a fails
// row (rows.md, Context 4; CTX-ROWS-2b): Live builds its Store, Broken's
// entry fails. A with of each writes try (With 6), and its caller catches
// the failure; Store's law runs under each.
const contextFailsSource = `type Missing { Name string }

type Store {
	Get() int,
	law Steady() { Get() == Get() },
}

type Disk { n int } implements Store

func Open(name string) Disk fails Missing {
	if name == "" {
		return Missing{Name: name}
	}
	Disk{n: 1}
}

func Get(d Disk) int { d.n }

let Live = context fails Missing { Store: try Open("db") }

let Broken = context fails Missing { Store: try Open("") }

func good() int fails Missing { with try Live { Store.Get() } }

func bad() int fails Missing { with try Broken { Store.Get() + 100 } }

func main() needs Console {
	let a = good() catch {
	case Missing:
		-1
	}
	let b = bad() catch {
	case Missing:
		-2
	}
	Console.Println("\(a) \(b)")
}
`

// TestFallibleContextLowers: the lowered Go of contextFailsSource
// type-checks, in process: a fallible context's builder returns error,
// and each with of it reads it (mutant ctxf2b-builder-unit).
func TestFallibleContextLowers(t *testing.T) {
	o := mustLower(t, "ctx_fails", contextFailsSource)
	typecheckGo(t, []*lowered{o})
}

// TestRunFallibleContext: a with of a context whose build fails leaves
// before its block runs, with the build's failure, which its caller
// catches; one whose build succeeds runs its block (rows.md, With 6).
// Mutants: ctxf2b-builder-panics (an entry's try panics in the builder),
// ctxf2b-with-unpropagated (the with runs its block over a failed build).
//
// It builds Go because it checks what the built program prints, the path
// a failed build takes out of the Go builder; the check-time evaluator
// builds contexts its own way (eval's acquire).
func TestRunFallibleContext(t *testing.T) {
	needsBuild(t)
	o := mustLower(t, "ctx_fails_run", contextFailsSource)
	exes := buildAll(t, []*lowered{o}, true)
	if exes == nil {
		t.Fatal("the lowered program does not build")
	}
	stdout, stderr, code := runExe(t, exes[o.name])
	if code != 0 || stdout != "1 -2\n" {
		t.Fatalf("exit %d, stdout:\n%s\nwant:\n1 -2\nstderr:\n%s", code, stdout, stderr)
	}
}

// TestLawUnderAFallibleContext: a law runs under a context whose build
// may fail; a case whose build fails is a failed case that names the
// context, never a skip or a refutation (FALLIBLE-CONTEXT Design 4;
// types.md, Laws 10). Mutant: ctxf2b-law-build-unchecked.
//
// It builds Go because the verdicts are the built test binary's report.
func TestLawUnderAFallibleContext(t *testing.T) {
	needsBuild(t)
	exes := buildAll(t, []*lowered{lowerTestBuild(t, "lawctx_fails", contextFailsSource)}, true)
	if exes == nil {
		return
	}
	stdout, stderr, code := runExe(t, exes["lawctx_fails"])
	for _, want := range []string{
		"ok law Store.Steady under Live (proved: 1 case)",
		"law context Broken could not be built: ",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout lacks %q:\n%s\nstderr:\n%s", want, stdout, firstLines(stderr, 8))
		}
	}
	if strings.Contains(stdout, "ok law Store.Steady under Broken") || strings.Contains(stdout, "skip law Store.Steady under Broken") || code == 0 {
		t.Errorf("the run under Broken did not fail (exit %d):\n%s", code, stdout)
	}
}
