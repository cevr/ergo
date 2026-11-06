package check_test

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// qualifiedLib is package lib of the qualified-embed tests: W binds Store to
// Mem{V: read()}; read requires the capability Log, and W binds Log too.
// The context w is unexported.
const qualifiedLib = `type Store { Get() string }
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
let w = context { Log: Quiet }
`

// qualifiedPackage checks qualifiedLib as package lib.
func qualifiedPackage(t *testing.T) *check.Package {
	t.Helper()
	f, err := syntax.Parse(qualifiedLib)
	if err != nil {
		t.Fatal(err)
	}
	lib, ds := check.Check("lib", []*syntax.File{f}, nil)
	if len(ds) != 0 {
		t.Fatalf("package lib: %v", ds)
	}
	return lib
}

// TestQualifiedContextEmbed (196b Q9): `context { lib.W }` means what a
// bare embedding of a named context means. An inline, a named and a local
// context of package main embed lib.W: they check with no diagnostic (no
// S104 at the embedding, no S110 cascade on the import), a replace may
// override what lib.W binds, and the footprint of the frame that builds
// one holds what lib.W's providers run (E5 C36): Log through read(). An
// unexported or a missing context of lib is S104 naming it, and only it.
func TestQualifiedContextEmbed(t *testing.T) {
	lib := qualifiedPackage(t)
	imp := shadowImporter{"lib": lib}
	src := `import lib

type Box { Peek() int }
type Lid {}
func Peek(_ Lid) int { 1 }
type Mine {}
func Get(_ Mine) string { "mine" }

func inline() string {
	with context { lib.W } { lib.Store.Get() }
}

func mixed() int {
	with context { Box: Lid, lib.W } { Box.Peek() }
}

let Over = context { lib.W, replace lib.Store: Mine }

func over() string {
	with Over { lib.Store.Get() }
}

let Plain = context { lib.W }

func plain() string {
	with Plain { lib.Store.Get() }
}

func local() string {
	let x = context { lib.W }
	with x { lib.Store.Get() }
}
`
	file, err := syntax.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	pkg, ds := check.Check("main", []*syntax.File{file}, imp)
	if len(ds) != 0 {
		t.Fatalf("package main: %v", ds)
	}
	// A with acquires what its block needs (With.Acquired): mixed's block
	// needs Box only, so lib.W's providers do not run there. A named
	// context's builder node reads an imported embedding as opaque
	// (footprint.go provides, outside this unit: a sound over-count; see
	// the ctx-pkg follow-ups); an inline or a local context's frame joins
	// the imported entries it acquires.
	want := map[string]string{
		"inline": "Log Store",
		"mixed":  "Box",
		"over":   "Store opaque",
		"plain":  "Store opaque",
		"local":  "Log Store",
	}
	for _, f := range pkg.Funcs {
		w, named := want[f.Name]
		if !named {
			continue
		}
		fp, ok := pkg.Info.Footprints[f.Decl]
		if !ok {
			t.Errorf("%s has no footprint", f.Name)
			continue
		}
		if got := show(fp); got != w {
			t.Errorf("%s: footprint %q, want %q", f.Name, got, w)
		}
	}
	// The replace overrides lib.W's Store entry; Log stays lib.W's.
	over := pkg.Contexts["Over"]
	if len(over.Overrides) != 1 || over.Overrides[0].Cap.String() != "lib.Store" {
		t.Errorf("Over: overrides %+v, want one of lib.Store", over.Overrides)
	} else if o := over.Overrides[0]; o.Entry == nil || o.Entry != lib.Contexts["W"].Entries[1] {
		t.Errorf("Over: the replace overrides %+v, want lib.W's Store entry", o.Entry)
	}
	for _, e := range lib.Contexts["W"].Entries {
		cap := e.Caps[0]
		got := over.Provider(cap)
		switch cap.String() {
		case "lib.Log":
			if got != e {
				t.Errorf("Over: lib.Log is provided by %+v, want lib.W's entry", got)
			}
		case "lib.Store":
			if got == nil || got == e || !strings.Contains(syntaxText(src, got.Provider), "Mine") {
				t.Errorf("Over: lib.Store is provided by %+v, want the replace Mine{}", got)
			}
		}
	}

	// An import used only by an embedding is used: no S110.
	for _, src := range []string{
		"import lib\nlet X = context { lib.W }\nfunc main() { with X { } }\n",
		"import lib\nfunc main() { with context { lib.W } { } }\n",
	} {
		f, err := syntax.Parse(src)
		if err != nil {
			t.Fatal(err)
		}
		if _, ds := check.Check("main", []*syntax.File{f}, imp); len(ds) != 0 {
			t.Errorf("%q: diagnostics %v, want none", src, ds)
		}
	}

	// The context pass resolves a qualified embedding through the imports
	// of the literal's own file: here the second file's. Were the summary
	// unknown, the with would discharge Console and hide the S102.
	{
		a, err := syntax.Parse("func main() { b() }\n")
		if err != nil {
			t.Fatal(err)
		}
		b, err := syntax.Parse("import lib\nfunc b() { with context { lib.W } {\n\tlet _ = lib.Store.Get()\n\tConsole.Println(\"x\")\n} }\n")
		if err != nil {
			t.Fatal(err)
		}
		_, ds := check.Check("main", []*syntax.File{a, b}, imp)
		// b's inferred row keeps Console, so main's call of b is the S102.
		if len(ds) != 1 || ds[0].Code != "S102" || ds[0].File != 0 || !strings.Contains(ds[0].Message, "Console") {
			t.Errorf("second file: diagnostics %v, want one S102 for Console at main's call of b", ds)
		}
	}

	// Refusals: each is S104 at the embedding and nothing else.
	for _, c := range []struct{ name, src, want string }{
		{"unexported", "import lib\nfunc main() { with context { lib.w } { } }\n", "lib.w is not an exported context of package lib"},
		{"missing", "import lib\nfunc main() { with context { lib.Nope } { } }\n", "lib.Nope is not an exported context of package lib"},
		{"named unexported", "import lib\nlet X = context { lib.w }\nfunc main() { with X { } }\n", "lib.w is not an exported context of package lib"},
		{"not a package", "type Q {}\nlet X = context { Q.W }\nfunc main() { with X { } }\n", "unknown context Q.W"},
	} {
		f, err := syntax.Parse(c.src)
		if err != nil {
			t.Fatal(err)
		}
		_, ds := check.Check("main", []*syntax.File{f}, imp)
		if len(ds) != 1 || ds[0].Code != "S104" || !strings.Contains(ds[0].Message, c.want) {
			t.Errorf("%s: diagnostics %v, want one S104 naming %q", c.name, ds, c.want)
		}
	}
}

// syntaxText is the source text of the node n of src.
func syntaxText(src string, n syntax.Node) string {
	return src[n.Pos():n.End()]
}
