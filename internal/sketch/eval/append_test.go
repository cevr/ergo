package eval

import "testing"

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

func put(b *buf, x byte) { b.out = push(b.out, x) }

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
