package check_test

import (
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// importedImporter serves one checked package b, and the standard library.
type importedImporter struct{ b *check.Package }

func (i importedImporter) Import(path string) (*check.Package, error) {
	if path == "b" {
		return i.b, nil
	}
	return check.Std.Import(path)
}

// TestImportedContextFootprint (check-v1 E5, C36): an imported context's
// provider expressions count in the importing package's view. Package b
// binds Store to Mem{V: read()}, and read needs Log, which W binds too. In
// package a, a with of b.W, of (b.W) and of b.W beside an inline context
// each give their function a footprint that holds Log (what the provider
// runs) and is not opaque; and b's Info holds the Calls fact of read() in
// W's provider.
func TestImportedContextFootprint(t *testing.T) {
	lib, err := syntax.Parse(`type Store { Get() string }
type Log { Say(s string) }
type Mem { V string }
func Get(m Mem) string { m.V }
func read() string needs Log {
	Log.Say("read")
	"x"
}
type Quiet {}
func Say(_ Quiet, s string) {}
let W = context { Log: Quiet, Store: Mem{V: read()} }
`)
	if err != nil {
		t.Fatal(err)
	}
	b, ds := check.Check("b", []*syntax.File{lib}, nil)
	if len(ds) != 0 {
		t.Fatalf("package b: %v", ds)
	}
	found := false
	for _, e := range b.Contexts["W"].Entries {
		if e.Provider == nil {
			continue
		}
		for x, call := range b.Info.Calls {
			if call.Func != nil && call.Func.Name == "read" && x.Pos() >= e.Provider.Pos() && x.End() <= e.Provider.End() {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("b's Info has no Calls fact of read() in W's provider")
	}
	// An inline context that embeds b.W is S104 at base and here: no
	// context literal embeds an imported context yet (state.md,
	// Follow-ups). The forms that name b.W as a with operand are pinned.
	src := `import b
type Box { Peek() int }
type Lid {}
func Peek(_ Lid) int { 1 }
func named() {
	with b.W { b.Store.Get() }
}
func paren() {
	with (b.W) { b.Store.Get() }
}
func several() int {
	with b.W, context { Box: Lid } { Box.Peek() }
}
`
	main, err := syntax.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	pkg, ds := check.Check("a", []*syntax.File{main}, importedImporter{b})
	if len(ds) != 0 {
		t.Fatalf("package a: %v", ds)
	}
	want := map[string]string{"named": "Log Store", "paren": "Log Store", "several": "Box Log"}
	for _, f := range pkg.Funcs {
		if _, named := want[f.Name]; !named {
			continue
		}
		fp, ok := pkg.Info.Footprints[f.Decl]
		if !ok {
			t.Errorf("%s has no footprint", f.Name)
			continue
		}
		if got := show(fp); got != want[f.Name] {
			t.Errorf("%s: footprint %q, want %q", f.Name, got, want[f.Name])
		}
	}
}
