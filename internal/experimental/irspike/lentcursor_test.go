package irspike

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// lentCursor lends a sealed reader of its own package, a Cursor, to
// decode[T] as the prelude's Reader inside a body with a codec witness,
// as module xml's Decode does (XML-ATTR).
const lentCursor = `import json

// Cursor is the prelude's Reader sealed here, as module xml's Cursor is:
// its one provider, cur, is a cell that decode[T] borrows.
type Cursor {
	..Reader,
	mut seal(),
}

type cur { inner json.Reader } implements Cursor

func seal(c mut cur) {}
func TakeNull(c mut cur) bool { c.inner.TakeNull() }
func Peek(c mut cur) WireKind { c.inner.Peek() }
func Number(c mut cur) string fails WireWrongKind { try c.inner.Number() }
func Path(c mut cur) string { c.inner.Path() }
func Bool(c mut cur) bool fails WireWrongKind { try c.inner.Bool() }
func Int(c mut cur) int64 fails WireWrongKind { try c.inner.Int() }
func Uint(c mut cur) uint64 fails WireWrongKind { try c.inner.Uint() }
func Float(c mut cur) float64 fails WireWrongKind { try c.inner.Float() }
func String(c mut cur) string fails WireWrongKind { try c.inner.String() }
func StringBytes(c mut cur, into []byte) []byte fails WireWrongKind { try c.inner.StringBytes(into) }
func BeginList(c mut cur) fails WireWrongKind { try c.inner.BeginList() }
func BeginObject(c mut cur) fails WireWrongKind { try c.inner.BeginObject() }
func Tag(c mut cur) string fails WireMissingField { try c.inner.Tag() }
func More(c mut cur) bool { c.inner.More() }
func Key(c mut cur) string { c.inner.Key() }
func Skip(c mut cur) { c.inner.Skip() }
func End(c mut cur) { c.inner.End() }

func load[T wire](text string) T fails json.Malformed | wired[T] needs wired[T] {
	let r Cursor = cur{inner: try json.Open(text, 0, 0)}
	try decode[T](r)
}

type point { X int, Name string }

func main() needs Console {
	let p = load[point]("{\"x\": 3, \"name\": \"a\"}") catch {
	case _:
		point{X: -1, Name: "failed"}
	}
	Console.Println("\(p.X) \(p.Name)")
}
`

// TestLentCursorBuilds: a program that lends its own sealed Cursor to
// decode[T] as the prelude's Reader builds on the IR path, load has an IR
// body (IFACE-CONV: the Cursor is converted to the Reader, ergoir's
// toiface of an interface; XML-ATTR kept it text), and it prints what the
// source path prints. Mutant: ir-codec-iface-unconverted (the call takes
// the IR with the Cursor as it is: the IR build fails).
//
// It builds Go because the failure under test is the IR compiler's check
// of the IR body, which only a build runs.
func TestLentCursorBuilds(t *testing.T) {
	src, ir := needsBuild(t)
	dir := t.TempDir()
	file := filepath.Join(dir, "p.ergo")
	if err := os.WriteFile(file, []byte(lentCursor), 0o644); err != nil {
		t.Fatal(err)
	}
	u, err := Load(file)
	if err != nil {
		t.Fatal(err)
	}
	sb, ib, rep := both(t, u, src, ir, dir, BuildOptions{})
	a, b := run(t, sb), run(t, ib)
	if a != b || a.stdout != "3 a\n" {
		t.Fatalf("source %+v, ir %+v, want both to print \"3 a\"", a, b)
	}
	if !slices.Contains(rep.IR, "load") {
		t.Errorf("load has no IR body (IR bodies: %v; fallback: %v)", rep.IR, rep.Fallback)
	}
}
