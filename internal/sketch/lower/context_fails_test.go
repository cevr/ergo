package lower

import (
	"strings"
	"testing"
)

// TestFallibleContextIsRefused: a named context whose header writes a
// fails row checks (check/contextneeds.go, CTX-ROWS-2), and the lowering
// refuses it by name, never lowers it partly: its builder returns no error
// until CTX-ROWS-2b. Mutant only it kills (testdata/mutants.json):
// ctxf2-lower-unrefused.
func TestFallibleContextIsRefused(t *testing.T) {
	const src = `type Missing { Name string }

type Store { Get() int }

type Disk { n int } implements Store

func Open(name string) Disk fails Missing {
	if name == "" {
		return Missing{Name: name}
	}
	Disk{n: 1}
}

func Get(d Disk) int { d.n }

let Live = context fails Missing { Store: try Open("db") }

func read() int fails Missing { with try Live { Store.Get() } }

func main() needs Console {
	let n = read() catch {
	case Missing:
		0
	}
	Console.Println("\(n)")
}
`
	o := lowerSource("ctx_fails", src, true)
	if o.err != nil || len(o.diags) > 0 || o.panicked != nil {
		t.Fatalf("lowering failed: %v %v %v", o.err, o.diags, o.panicked)
	}
	const want = "context Live can fail while it is built (its header's fails row); a builder that returns the failure is CTX-ROWS-2b's"
	for _, r := range o.refused {
		if strings.Contains(r.Message, want) {
			return
		}
	}
	t.Fatalf("refused %+v, want a refusal %q", o.refused, want)
}
