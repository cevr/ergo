package selfhostcheck

import (
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/hostsyntax"
)

// chain is a declaration whose value is a binary expression of n operands,
// alternating precedences 4 and 5 so that gofmt's cutoff walks the whole
// chain from every operator.
func chain(n int) string {
	var b strings.Builder
	b.WriteString("let x = a0")
	for i := 1; i < n; i++ {
		if i%2 == 0 {
			b.WriteString(" + a")
		} else {
			b.WriteString(" * a")
		}
		b.WriteString(strconv.Itoa(i))
	}
	b.WriteString("\n")
	return b.String()
}

// TestFormatWorkGrowsLinearly is the formatter's growth guard (capstone
// S1b): the printer's work counter (the walks of binary expressions it
// computes, hostsyntax.Work) at n and 2n operands of one chain grows by at
// most 2.2 times. go/printer's cutoff asks walkBinary for every operator of
// a chain; the memo by row keeps the walks linear in the chain's length.
//
// Mutant it alone catches: sh-walk-memo-dropped (internal/selfhostcheck
// ledger): no output changes, the walks become quadratic.
func TestFormatWorkGrowsLinearly(t *testing.T) {
	const n = 200
	small, err := hostsyntax.Work(chain(n))
	if err != nil || small <= 0 {
		t.Fatalf("work at %d operands: %d, %v", n, small, err)
	}
	large, err := hostsyntax.Work(chain(2 * n))
	if err != nil || large <= 0 {
		t.Fatalf("work at %d operands: %d, %v", 2*n, large, err)
	}
	if float64(large) > 2.2*float64(small) {
		t.Errorf("the formatter's walks grow %d -> %d (%.2fx) from %d to %d operands; at most 2.2x", small, large, float64(large)/float64(small), n, 2*n)
	}
}
