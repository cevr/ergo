package check_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"ergo.local/prototype/internal/sketch/check"
)

// Typed self-call edges (round 6 review M1): a call x.f(…) of a 196m
// function adds one body-graph edge, to the function typing resolves it
// to, whatever spells the receiver. An edge by name to every function
// named f merged unrelated bodies (false class G and initialization-cycle
// refusals) and made k same-named functions called m times k·m edges.

// TestSelfCallEdgesLinear: n types each with a function m, and n callers
// that reach m through a local bound to a call's result (the reviewer's
// gen.sh). Each caller has two edges, to its maker and to its m, so the
// body graph has exactly 2n edges at n = 1000, 2000 and 4000: linear, where
// edges by name grew as n².
func TestSelfCallEdgesLinear(t *testing.T) {
	for _, n := range []int{1000, 2000, 4000} {
		var b strings.Builder
		for i := range n {
			fmt.Fprintf(&b, "type R%d {}\nfunc mk%d() R%d { return R%d }\nfunc m(_ R%d) {}\nfunc f%d() {\n\tlet x = mk%d()\n\tx.m()\n}\n", i, i, i, i, i, i, i)
		}
		b.WriteString("func main() {}\n")
		start := time.Now()
		edges, diags := check.BodyGraphEdges(b.String())
		if len(diags) != 0 || edges != 2*n {
			t.Fatalf("n = %d: %d body edges (want %d), %v", n, edges, 2*n, diags)
		}
		t.Logf("n = %d: %d edges, %s", n, edges, time.Since(start))
	}
}
