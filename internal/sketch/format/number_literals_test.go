package format

import (
	"strings"
	"testing"
)

// TestNumberLiteralSpelling (L1): ergo fmt keeps the spelling of a
// number literal, its base prefix, the case of its digits and its digit
// separators, and the formatted text parses back to the same tree.
func TestNumberLiteralSpelling(t *testing.T) {
	lits := []string{"0x80", "0XFF_FF", "0xdead_beef", "0o600", "0O7", "0b1010_0101", "0B1", "1_000_000", "1_000.000_5", "1e1_0", "1_500ms", "0x_1F"}
	src := "func f() {\n"
	for _, lit := range lits {
		src += "\tlet _ = " + lit + "\n"
	}
	src += "}\n"
	out := check(t, "number literals", src, 3)
	for _, lit := range lits {
		if !strings.Contains(out, " "+lit+"\n") {
			t.Errorf("the literal %s lost its spelling:\n%s", lit, out)
		}
	}
	if out != src {
		t.Errorf("formatting changed a formatted source:\n%s", out)
	}
}
