package lower

import (
	"strings"
	"testing"
)

// contextNeedsSource builds named contexts whose header writes a needs row
// (rows.md, Context 4; CTX-ROWS-1): each provider reads the Clock of the
// with site that builds it, a with of the context, a with of a context
// that embeds it, and a Context value made from it, under different
// Clocks, and a function whose row lists Clock passes its caller's on.
const contextNeedsSource = `type Clock { Now() int }

type Store { Get() int }

type Timed { at int } implements Store

func Get(t Timed) int { t.at }

func NewTimed() Timed needs Clock { Timed{at: Clock.Now()} }

type Fixed { n int } implements Clock

func Now(f Fixed) int { f.n }

let Live = context needs Clock { Store: NewTimed() }

let Outer = context needs Clock { Live }

func read() int needs Clock { with Live { Store.Get() } }

func main() needs Console {
	with context { Clock: Fixed{n: 7} } {
		Console.Println("\(with Live { Store.Get() })")
		Console.Println("\(with Outer { Store.Get() })")
		Console.Println("\(read())")
		let v Context[Store] = Live
		with context { Clock: Fixed{n: 9} } {
			Console.Println("\(with Live { Store.Get() })")
			Console.Println("\(with v { Store.Get() })")
		}
	}
}
`

// TestContextNeedsRowLowers: the lowered Go of contextNeedsSource
// type-checks, in process.
func TestContextNeedsRowLowers(t *testing.T) {
	o := mustLower(t, "ctx_needs", contextNeedsSource)
	typecheckGo(t, []*lowered{o})
}

// TestRunContextNeedsRow: each build of a named context reads the Clock of
// the with site that builds it, through a with, an embedding and a
// function's row, and a Context value keeps the providers of the place
// that made it.
//
// It builds Go because it checks what the built program prints, the value
// the builder read from the with site's environment; the check-time
// evaluator's agreement is TestContextNeedsRowModel (sketchfront), and no
// in-process check observes the Go builder's reads.
func TestRunContextNeedsRow(t *testing.T) {
	needsBuild(t)
	o := mustLower(t, "ctx_needs_run", contextNeedsSource)
	exes := buildAll(t, []*lowered{o}, true)
	if exes == nil {
		t.Fatal("the lowered program does not build")
	}
	stdout, stderr, code := runExe(t, exes[o.name])
	want := strings.Join([]string{"7", "7", "7", "9", "7"}, "\n") + "\n"
	if code != 0 || stdout != want {
		t.Fatalf("exit %d, stdout:\n%s\nwant:\n%s\nstderr:\n%s", code, stdout, want, stderr)
	}
}
