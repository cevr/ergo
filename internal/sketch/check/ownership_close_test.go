package check

import (
	"strconv"
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

// TestGenericDropRefused is the text and position row of the refusal
// that stands in for Gap 42 (#197 L8 B2): a generic body that drops a
// value of its type parameter cannot release it, so the drop is a copy
// site and an instance with a resource is S108; with plain data it is
// admitted.
//
// Mutant it catches that nothing else does: a dropped T recorded as no
// copy site (the resource leaks silently).
func TestGenericDropRefused(t *testing.T) {
	base := strings.Count(ofPrelude, "\n")
	src := ofPrelude + `func drop[T](x T) takes x {}

func main() {
	drop(1)
	drop(open(1))
}
`
	_, diags := checkFor(src)
	want := "drop cannot take a Res: drop copies its T at " + strconv.Itoa(base+1) + ":14 (x is dropped at the end of its scope, and a generic body does not release a value of its type parameter yet (#197 Gap 42))"
	if len(diags) != 1 || diags[0].Code != CodeType || diags[0].Line != base+5 || diags[0].Column != 2 || !strings.Contains(diags[0].Message, want) {
		t.Errorf("got %+v, want one S108 at %d:2 containing %q", diags, base+5, want)
	}
}

// TestArrayOwnership is the smallest repro of a bug the batch-16a merge
// fixed: B3t deleted the task-only walk, the one place that knew a fixed
// array ([N]T, L1) holds what its element holds, so an array of resources
// had no owner: a use after its hand-off and an element taken out by index
// were admitted (resources R15, R35). The array now holds a resource, and
// its generated release releases the elements last first, as a slice's.
//
// Mutants it catches that nothing else does: the resource walk without its
// Array edge (b16a-array-holds-no-resource), and an element of an array
// taken by index admitted (b16a-array-element-take-admitted).
func TestArrayOwnership(t *testing.T) {
	base := strings.Count(ofPrelude, "\n")
	src := ofPrelude + `func main() {
	let a = [2]Res{open(1), open(2)}
	keep(a[0])
	let b = a
	lend(b[1])
	lend(a[1])
}
`
	_, diags := checkFor(src)
	type row struct {
		line, col int
		text      string
	}
	want := []row{
		{base + 3, 7, "a[…] cannot be handed off: it is an element of a, lent by the index"},
		{base + 6, 7, "a was handed off at " + strconv.Itoa(base+4) + ":10 (let moves it) and cannot be used after that"},
	}
	ok := len(diags) == len(want)
	for i := 0; ok && i < len(want); i++ {
		d := diags[i]
		ok = d.Code == CodeType && d.Line == want[i].line && d.Column == want[i].col && strings.Contains(d.Message, want[i].text)
	}
	if !ok {
		t.Errorf("got %+v, want S108 rows %+v", diags, want)
	}
}

// TestNoReleaseRefused is the text and position row of the refusal of an
// owned value whose type keeps resources and has no release (#197 L8 B2,
// B3): a user operation type whose operation takes a resource and that has
// no Close() (resources R1, R13). The std queue has its drain; this one
// has nothing that could release what it keeps.
//
// Mutant it catches that nothing else does: such a value admitted (its
// resources leak silently).
func TestNoReleaseRefused(t *testing.T) {
	base := strings.Count(ofPrelude, "\n")
	src := ofPrelude + `type Keeper[T] { Put(v T) takes v }

type Mem { n int }

func Put(m Mem, v Res) takes v { Close(v) }

func main() {
	let b Keeper[Res] = Mem{n: 1}
	b.Put(open(1))
}
`
	_, diags := checkFor(src)
	want := "Keeper[Res] has no release: it holds Keeper[Res], an operation type that keeps the values it takes and has no Close() operation"
	if len(diags) != 1 || diags[0].Code != CodeType || diags[0].Line != base+8 || diags[0].Column != 6 || !strings.Contains(diags[0].Message, want) {
		t.Errorf("got %+v, want one S108 at %d:6 containing %q", diags, base+8, want)
	}
}
