package check

import (
	"strings"
	"testing"
)

// TestCloseFormsTakeOnce is the smallest repro of B1's double release
// through a Close reached another way (#197 L8 B1 known gap): `(&h).Close()`
// did not take h, so h was closed by hand and again at its scope end; and
// a method value of Close (`let f = r.Close`) could be called twice or
// never. The address form now takes h (a later use is S108); the method
// value is refused with its text and position (resources R33: a function
// value owns no resource).
//
// Mutants it catches that nothing else does: a Close through `&h` that
// takes nothing (the borrowed() UnaryExpr arm removed); a method value of
// Close admitted.
func TestCloseFormsTakeOnce(t *testing.T) {
	base := strings.Count(ofPrelude, "\n")
	src := ofPrelude + `type Releaser { Close() }

func addr() {
	let mut h = open(1)
	(&h).Close()
	lend(h)
}

func value() {
	let r Releaser = open(2)
	let f = r.Close
	f()
}

func main() {}
`
	_, diags := checkFor(src)
	type want struct {
		line, col int
		text      string
	}
	wants := []want{
		{base + 6, 7, "h was handed off at"},
		{base + 11, 10, "r.Close is a method value of Close, which would own r; a function value owns no resource"},
	}
	if len(diags) != len(wants) {
		t.Fatalf("got %d diagnostics %+v, want %d", len(diags), diags, len(wants))
	}
	for i, w := range wants {
		d := diags[i]
		if d.Code != CodeType || d.Line != w.line || d.Column != w.col || !strings.Contains(d.Message, w.text) {
			t.Errorf("diagnostic %d: got %+v, want S108 at %d:%d containing %q", i, d, w.line, w.col, w.text)
		}
	}
}
