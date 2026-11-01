package check

import (
	"testing"

	"ergo.local/prototype/internal/sketch/syntax"
)

// Named wires cannot fail during construction yet. Exercise the same
// checker fact on the fallible constructors stage 3c's inline wires need,
// including the implicit try tail and failure returns that yield no value.
func TestWireOwnershipFreshFallibleConstructors(t *testing.T) {
	f, err := syntax.Parse(`import queue
type Conn { n int }
type Fault {}
func openA() Conn { Conn{n: 1} }
func openB(ok bool) Conn fails Fault {
	if !ok { return Fault }
	openA()
}
func wrap(ok bool) Conn fails Fault { try openB(ok) }
func reuse() Conn fails Fault { let c = try openB(true); c }
func newQueue() queue.Queue[int] { queue.New[int](1) }
`)
	if err != nil {
		t.Fatal(err)
	}
	pkg, ds := Check("main", []*syntax.File{f}, nil)
	if len(ds) != 0 {
		t.Fatal(ds)
	}
	fresh := providerFreshFunctions(pkg.Funcs)
	want := map[string]bool{"openA": true, "openB": true, "wrap": true, "reuse": false, "newQueue": true}
	for _, fn := range pkg.Funcs {
		if fresh[fn] != want[fn.Name] {
			t.Errorf("%s fresh=%v, want %v", fn.Name, fresh[fn], want[fn.Name])
		}
		delete(want, fn.Name)
	}
	if len(want) != 0 {
		t.Fatalf("missing constructors: %v", want)
	}
}
