package check

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/syntax"
)

// TestCloseFormsTakeOnce is the smallest repro of B1's double release
// through a Close reached another way (L8 B1 known gap): `(&h).Close()`
// did not take h, so h was closed by hand and again at its scope end; and
// a method value of Close (`let f = r.Close`) could be called twice or
// never. The address form now takes h (a later use is S108); the method
// value is refused with its text and position (resources R33: a function
// value owns no resource). A user operation type's `Close()` takes the
// value it is called on, so a resource's written Close implements it.
//
// Mutants it catches that nothing else does: a Close through `&h` that
// takes nothing (the borrowed() UnaryExpr arm removed); a method value of
// Close admitted; an operation type's Close that takes nothing (L8-B2,
// closeOpTakes: Pipe's clause would be S106).
func TestCloseFormsTakeOnce(t *testing.T) {
	base := strings.Count(ofPrelude, "\n")
	src := ofPrelude + `// r is a Closer, which Res declares.

func addr() {
	let mut h = open(1)
	(&h).Close()
	lend(h)
}

func value() {
	let r Closer = open(2)
	let f = r.Close
	f()
}

// Stream lists its own Close(), which takes the value it is called on,
// as Pipe's Close does: the clause holds.
type Stream { Close(), Get() int }

type Pipe { n int } implements Stream, Closer

func Get(p Pipe) int { p.n }

func Close(p Pipe) {}

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
// that stands in for Gap 42 (L8 B2): a generic body that drops a
// value of its own type parameter cannot release it, so the drop is S108
// in the body (Generics 9); the calls, with plain data and with a
// resource, are admitted by the mark.
//
// Mutant it catches that nothing else does: a dropped own T admitted (the
// resource leaks silently).
func TestGenericDropRefused(t *testing.T) {
	base := strings.Count(ofPrelude, "\n")
	src := ofPrelude + `func drop[T own](x own T) {}

func main() {
	drop(1)
	drop(open(1))
}
`
	_, diags := checkFor(src)
	want := "x is dropped at the end of its scope, and a generic body does not release a value of its type parameter yet (#197 Gap 42), and T is marked own"
	if len(diags) != 1 || diags[0].Code != CodeType || diags[0].Line != base+1 || diags[0].Column != 18 || !strings.Contains(diags[0].Message, want) {
		t.Errorf("got %+v, want one S108 at %d:18 containing %q", diags, base+1, want)
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
// owned value whose type keeps resources and has no release (resource ownership L8 B2,
// B3): a user operation type whose operation takes a resource and that has
// no Close() (resources R1, R13). The std queue includes the prelude's
// Keeping; this one does not, so a drain of Keeping's shape is an ordinary
// operation and nothing could release what it keeps (STD-NOPRIV NP3c).
//
// The repair it names is `..Closer` at Keeper's declaration (R1: only an
// operation type that includes Closer releases).
//
// Mutants it catches that nothing else does: such a value admitted (its
// resources leak silently); keeps-read-by-name (a drain operation counts
// without the Keeping include); the repair that names no declaration.
func TestNoReleaseRefused(t *testing.T) {
	base := strings.Count(ofPrelude, "\n")
	src := ofPrelude + `type Keeper[T own] { Put(v own T), own drain() []T }

type Mem { n int } implements Keeper[Res]

func Put(m Mem, v own Res) { Close(v) }

func drain(m Mem) []Res { []Res{} }

func main() {
	let b Keeper[Res] = Mem{n: 1}
	b.Put(open(1))
}
`
	_, diags := checkFor(src)
	want := "Keeper[Res] has no release: it holds Keeper[Res], an operation type that keeps the values it takes and does not include Closer, so nothing could release them (resources R1, R13); add ..Closer to Keeper (" + strconv.Itoa(base+1) + ":6)"
	if len(diags) != 1 || diags[0].Code != CodeType || diags[0].Line != base+10 || diags[0].Column != 6 || diags[0].Message != want {
		t.Errorf("got %+v, want one S108 at %d:6: %q", diags, base+10, want)
	}
}

// TestR22NamesCloser is the text and position row of R22's repair (the
// diagnostic's text): an operation type that lists Close() but does not
// include Closer releases nothing (R1), so the refusal names `..Closer` at
// the type's declaration, in its own file, not "give Stream a Close()
// operation", which Stream already has.
func TestR22NamesCloser(t *testing.T) {
	var files []*syntax.File
	for _, nf := range [][2]string{
		{"pipe.ergo", "type Pipe { n int } implements Stream, Closer\n\nfunc Close(p Pipe) {}\n\nfunc Get(p Pipe) int { p.n }\n\nfunc main() {\n\tlet s Stream = Pipe{n: 1}\n\t_ = s\n}\n"},
		{"stream.ergo", "// Stream lists a Close.\ntype Stream { Close(), Get() int }\n"},
	} {
		f, err := syntax.Parse(nf[1])
		if err != nil {
			t.Fatal(err)
		}
		f.Name = nf[0]
		files = append(files, f)
	}
	_, ds := Check("main", files, nil)
	want := "8:17 S108 cannot use Pipe as Stream in let s: Pipe is a resource and Stream does not include Closer, so the value would never be released (resources R22); add ..Closer to Stream (stream.ergo:2:6)"
	var got []string
	for _, d := range ds {
		got = append(got, fmt.Sprintf("%d:%d %s %s", d.Line, d.Column, d.Code, d.Message))
	}
	if strings.Join(got, "\n") != want {
		t.Errorf("got  %s\nwant %s", strings.Join(got, "\n     "), want)
	}
}
