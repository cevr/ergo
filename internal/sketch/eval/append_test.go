package eval

import (
	"strconv"
	"strings"
	"testing"
)

// TestAppendStringCountsItsBytes is a guard over counted work: Go's
// append(out, s...) bound by an extern (std civil's formats, cborpack)
// does work in the bytes it appends, so 50 appends of a 1000-byte text
// spend at least 50×990 more steps than 50 appends of a 10-byte one, and
// each append extends the buffer in place, as Go's does. Charged one step
// a call, with a copy of the whole buffer each time (before MX-EVBUILDER),
// the evaluator did quadratic work the budget never saw. It kills
// evbuilder-appendstring-copies (testdata/mutants.json).
func TestAppendStringCountsItsBytes(t *testing.T) {
	src := func(lit string) string {
		return `extern go append func appendString(out []byte, s string) []byte

func fill() int {
	let mut out = []byte{}
	for _ in 0..50 {
		out = appendString(out, "` + lit + `")
	}
	len(out)
}

test Fill {
	assert fill() == 50 * ` + strconv.Itoa(len(lit)) + `
}
`
	}
	steps := func(lit string) int64 {
		rep, err := Run(checked(t, src(lit)), DefaultBudget())
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range rep.Results {
			if r.Name == "Fill" {
				if r.Status != Passed {
					t.Fatalf("Fill (%d bytes): %s (%s)", len(lit), r.Status, r.Reason)
				}
				return r.Steps
			}
		}
		t.Fatalf("Fill did not report: %+v", rep.Results)
		return 0
	}
	short, long := steps(strings.Repeat("x", 10)), steps(strings.Repeat("x", 1000))
	if long-short < 50*990 {
		t.Errorf("50 appends spend %d steps of 10 bytes and %d of 1000: the bytes appended are not counted", short, long)
	}
}

// TestBuiltinAppendRunsAtCheckTime: an extern of Go's append at its first
// shape, `[T](xs []T, x T) []T` (check/builtin.go), runs at check time as
// the prelude's xs.Append(x) does, element type generic, through a
// borrow's field (cborpack's head: t.out = appendByte(t.out, b)). Before
// MX-EVAPPEND it was skipped ("a Go function (push), even a pure one"),
// so cborpack's HeadsAtTheirEdges never ran at check time. It kills
// evappend-skipped and evappend-args-swapped (testdata/mutants.json):
// cborpack's verdict at check time is a skip either way, so no example
// reaches them.
func TestBuiltinAppendRunsAtCheckTime(t *testing.T) {
	pkg := checked(t, `extern go append func push[T](xs []T, x T) []T

type buf { out []byte }

func put(b mut buf, x byte) { b.out = push(b.out, x) }

test Bytes {
	let mut b = buf{out: []byte{}}
	put(&b, 1)
	put(&b, 2)
	put(&b, 3)
	assert len(b.out) == 3
	assert b.out[0] == 1
	assert b.out[2] == 3
}

test Strings {
	let xs = push(push([]string{"a"}, "b"), "c")
	assert len(xs) == 3
	assert xs[1] == "b"
	assert xs[2] == "c"
}
`)
	rep, err := Run(pkg, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"Bytes": true, "Strings": true}
	seen := 0
	for _, r := range rep.Results {
		if !want[r.Name] {
			continue
		}
		seen++
		if r.Status != Passed {
			t.Errorf("%s: %s (%s), want passed", r.Name, r.Status, r.Reason)
		}
	}
	if seen != len(want) {
		t.Errorf("%d of the %d tests reported: %+v", seen, len(want), rep.Results)
	}
}
