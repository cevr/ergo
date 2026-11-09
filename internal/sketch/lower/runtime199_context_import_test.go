package lower

import (
	"fmt"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

type contextImporter struct{ pkg *check.Package }

func (i contextImporter) Import(path string) (*check.Package, error) {
	if path == "makers" {
		return i.pkg, nil
	}
	return check.Std.Import(path)
}

// The checker records an imported context's entries and admits a fresh one.
// Until 196b U4 this lowering refused a with of an imported context, direct
// or embedded in it, as a located refusal (stage 3b round 3 review, m1).
// That test is inverted, not deleted: the unit now sources the imported
// package, so the with builds the imported provider, runs its block and
// closes the provider once.
func TestRuntime199ContextImportedBuilds(t *testing.T) {
	needsBuild(t)
	for _, embedded := range []bool{false, true} {
		t.Run(fmt.Sprintf("embedded=%v", embedded), func(t *testing.T) {
			contexts := "let W = context { A: Get() }\n"
			if embedded {
				contexts = "let Base = context { A: Get() }\nlet W = context { Base }\n"
			}
			libSrc := `type A { Use() }
type Conn { n int }
func Use(_ Conn) {}
func Close(_ Conn) needs Console { Console.Println("closed") }
func Get() Conn { Conn{n: 1} }
` + contexts
			lib, err := syntax.Parse(libSrc)
			if err != nil {
				t.Fatal(err)
			}
			makers, ds := check.Check("makers", []*syntax.File{lib}, nil)
			if len(ds) != 0 {
				t.Fatal(ds)
			}
			src := "import makers\nfunc main() needs Console { with makers.W { makers.A.Use() }\nConsole.Println(\"end\") }\n"
			file, err := syntax.Parse(src)
			if err != nil {
				t.Fatal(err)
			}
			pkg, ds := check.Check("main", []*syntax.File{file}, contextImporter{makers})
			if len(ds) != 0 {
				t.Fatalf("fresh imported context refused by the checker: %v", ds)
			}
			for _, w := range pkg.Info.Withs {
				if len(w.Contexts) != 1 || len(w.Acquired) != 1 {
					t.Fatalf("the checker no longer records the imported context's entry: %+v", w)
				}
			}
			lu := Unit{Package: makers, Files: []*syntax.File{lib}, Sources: []Source{{Path: "/ergo-test/makers/lib.ergo", Text: libSrc}}}
			u := Unit{Package: pkg, Files: []*syntax.File{file}, Sources: []Source{{Path: testSourcePath, Text: src}}, Imports: []Unit{lu}}
			name := "ctx_import_plain"
			if embedded {
				name = "ctx_import_embedded"
			}
			o := mustLowerUnit(t, name, u, false)
			exes := buildAll(t, []*lowered{o}, true)
			if exes == nil {
				t.FailNow()
			}
			stdout, stderr, code := runExe(t, exes[o.name])
			if code != 0 || stdout != "closed\nend\n" {
				t.Fatalf("exit %d, stdout %q, want %q; stderr:\n%s", code, stdout, "closed\nend\n", stderr)
			}
		})
	}
}
