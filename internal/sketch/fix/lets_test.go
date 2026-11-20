package fix

import (
	"testing"

	"ergo.local/prototype/internal/sketch/syntax"
)

// TestLetsRewritesGoLocals: ergo fix's let pass turns every Go local
// declaration of a source into the let form its S001 names, the output
// parses, and a second pass changes nothing (the owner's var tip of
// 2026-11-20).
func TestLetsRewritesGoLocals(t *testing.T) {
	src := "func f() int {\n\tvar a = 0\n\tvar b int = 1\n\tvar c int\n\td := 2\n\ta = a + b\n\tc += d\n\ta + c\n}\n"
	want := "func f() int {\n\tlet mut a = 0\n\tlet b int = 1\n\tlet mut c int\n\tlet d = 2\n\ta = a + b\n\tc += d\n\ta + c\n}\n"
	out, n := Lets(src)
	if out != want || n != 4 {
		t.Fatalf("%d rewrites:\n%s\nwant 4:\n%s", n, out, want)
	}
	if _, err := syntax.Parse(out); err != nil {
		t.Fatal(err)
	}
	if again, k := Lets(out); again != out || k != 0 {
		t.Fatalf("second pass: %d rewrites", k)
	}
}
