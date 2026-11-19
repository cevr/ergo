package check_test

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// atomicsSrc makes Go atomics in frames with no row: every method of
// atomic.Int64 and atomic.Bool is a memory-only row (GBX6).
const atomicsSrc = `import go sync/atomic

func Counter() go.Ref[atomic.Int64] {
	go.New[atomic.Int64]()
}

func Flag() go.Ref[atomic.Bool] {
	go.New(go.Zero[atomic.Bool]())
}
`

// TestGoNewOfMemoryOnlyChargesNothing is GBX1b's acceptance (SQ's NP4f-4:
// std's atomic.NewInt64 and NewBool are called from laws and tests, which
// hold no go.Foreign; interop.md, Go values by address 3): go.New of a Go
// type that reaches no host-touching method checks with no row, and its
// frame is still not pure (its footprint is opaque, as a memory-only
// call's is), so no pure frame evaluates it once for two uses. A Go type
// with a foreign method (sync.Mutex's Lock waits) still charges go.Foreign.
// Red at base: S102 at each go.New.
//
// Mutant it alone catches: gbx1b-new-opaque (the go.New call adds nothing
// to its frame's footprint, so the frame reads as pure).
func TestGoNewOfMemoryOnlyChargesNothing(t *testing.T) {
	f, err := syntax.Parse(atomicsSrc)
	if err != nil {
		t.Fatal(err)
	}
	pkg, ds := check.Check("example.test/atomics", []*syntax.File{f}, nil)
	for _, d := range ds {
		t.Errorf("want clean, got %s", d)
	}
	if pkg == nil || pkg.Info == nil {
		t.Fatal("no package info")
	}
	seen := 0
	for _, fn := range pkg.Funcs {
		fp, ok := pkg.Info.Footprints[fn.Decl]
		if !ok {
			t.Errorf("%s: no footprint", fn.Name)
			continue
		}
		seen++
		if fp.Empty() {
			t.Errorf("%s: its footprint is pure; a go.New makes a value with identity", fn.Name)
		}
	}
	if seen != 2 {
		t.Errorf("found %d of the two functions", seen)
	}

	const mutex = "import go sync\n\nfunc Lock() go.Ref[sync.Mutex] {\n\tgo.New[sync.Mutex]()\n}\n"
	f, err = syntax.Parse(mutex)
	if err != nil {
		t.Fatal(err)
	}
	_, ds = check.Check("example.test/mutex", []*syntax.File{f}, nil)
	if len(ds) != 1 || ds[0].Code != "S102" || ds[0].Line != 4 || !strings.Contains(ds[0].Message, "go.Foreign") {
		t.Errorf("go.New[sync.Mutex]() in a frame with no row: want one S102 at line 4 naming go.Foreign, got %v", ds)
	}
}
