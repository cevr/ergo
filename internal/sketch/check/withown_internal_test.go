package check

import (
	"maps"
	"slices"
	"testing"

	"ergo.local/prototype/internal/sketch/std"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Named wires cannot fail during construction yet. Exercise the same
// checker fact on the fallible constructors stage 3c's inline wires need,
// including the implicit try tail and failure returns that yield no value:
// a unit value, a literal, a package error value, a generic one, a failure
// tail and rethrows of a catch binding. The test reads the fact the
// ownership pass computed during Check, before the package published its
// error types, so it sees what decided.
func TestWireOwnershipFreshFallibleConstructors(t *testing.T) {
	f, err := syntax.Parse(`import queue
type Conn { n int }
type Fault { code int }
type Other {}
type GFault[T] { v T }
let gone = Fault{code: 7}
let ggone = GFault[int]{v: 1}
let shared = Conn{n: 1}
func risky() int fails Fault { return Fault{code: 1} }
func either() int fails Fault | Other { return Other }
func openA() Conn { Conn{n: 1} }
func openB(ok bool) Conn fails Fault {
	if !ok { return Fault{code: 2} }
	openA()
}
func openUnit(ok bool) Conn fails Other {
	if !ok { return Other }
	openA()
}
func openGone(ok bool) Conn fails Fault {
	if !ok { return gone }
	openA()
}
func openGeneric(ok bool) Conn fails GFault[int] {
	if !ok { return ggone }
	openA()
}
func openTail(ok bool) Conn fails Fault {
	if ok { return openA() }
	gone
}
func openRethrow() Conn fails Fault {
	let n = risky() catch { case Fault as e: return e }
	Conn{n: n}
}
func openRethrowAll() Conn fails Fault | Other {
	let n = either() catch { case e: return e }
	Conn{n: n}
}
func wrap(ok bool) Conn fails Fault { try openB(ok) }
func reuse() Conn fails Fault { let c = try openB(true); c }
func borrowGone(ok bool) Conn fails Fault {
	if !ok { return gone }
	shared
}
func newQueue() queue.Queue[int] { queue.New[int](1) }
`)
	if err != nil {
		t.Fatal(err)
	}
	pkg, ds := Check("main", []*syntax.File{f}, nil)
	if len(ds) != 0 {
		t.Fatal(ds)
	}
	fresh := pkg.providerFresh
	if fresh == nil {
		t.Fatal("the ownership pass recorded no freshness fact")
	}
	want := map[string]bool{
		"openA": true, "openB": true, "openUnit": true, "openGone": true,
		"openGeneric": true, "openTail": true, "openRethrow": true,
		"openRethrowAll": true, "wrap": true,
		"reuse": false, "borrowGone": false,
		"newQueue": false, // a bodiless extern proves nothing (#197)
	}
	for _, fn := range pkg.Funcs {
		w, ok := want[fn.Name]
		if !ok {
			continue
		}
		if fresh[fn] != w {
			t.Errorf("%s fresh=%v, want %v", fn.Name, fresh[fn], w)
		}
		delete(want, fn.Name)
	}
	if len(want) != 0 {
		t.Fatalf("missing constructors: %v", want)
	}
}

// No declaration says what a bodiless extern returns (prelude Delete
// returns its argument), so the freshness fact trusts none of them. List
// every bodiless extern of the prelude and of each std package, so a new
// one cannot become trusted by accident.
func TestWireOwnershipNoExternTrust(t *testing.T) {
	pkgs := []*Package{prelude().pkg}
	for _, path := range std.Packages() {
		p, err := Std.Import(path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		pkgs = append(pkgs, p)
	}
	var externs []*Func
	names := map[string]bool{}
	for _, p := range pkgs {
		for _, fn := range p.Funcs {
			if fn.Decl != nil && fn.Decl.Body == nil && fn.Decl.Extern != nil {
				externs = append(externs, fn)
				names[p.Path+"."+fn.Name] = true
			}
		}
	}
	for _, name := range []string{std.PreludePath + ".Delete", std.PreludePath + ".Append", "queue.New", "slice.WithCapacity"} {
		if !names[name] {
			t.Fatalf("extern list misses %s; listed %d externs", name, len(externs))
		}
	}
	fresh := providerFreshFunctions(externs, func(Type) bool { return false })
	for _, fn := range externs {
		if fresh[fn] {
			t.Errorf("bodiless extern %s.%s is trusted as fresh; an extern proves no freshness until #197 gives it an ownership fact", fn.Pkg.Path, fn.Name)
		}
	}
	t.Logf("%d bodiless std and prelude externs, none trusted: %v", len(externs), slices.Sorted(maps.Keys(names)))
}
