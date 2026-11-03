package lower

import (
	"errors"
	"fmt"
	"strings"
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

// The checker records an imported context's entries and admits a fresh one,
// but this lowering builds only the contexts of the file it lowers. A with of
// an imported context, direct or embedded in it, stays a located refusal, not
// an internal compiler error at naming (stage 3b round 3 review, m1).
func TestRuntime199ContextImportedRefusal(t *testing.T) {
	for _, embedded := range []bool{false, true} {
		t.Run(fmt.Sprintf("embedded=%v", embedded), func(t *testing.T) {
			contexts := "let W = wire { A: Get() }\n"
			if embedded {
				contexts = "let Base = wire { A: Get() }\nlet W = wire { Base }\n"
			}
			lib, err := syntax.Parse(`type A { Use() }
type Conn { n int }
func Use(_ Conn) {}
func Close(_ Conn) {}
func Get() Conn { Conn{n: 1} }
` + contexts)
			if err != nil {
				t.Fatal(err)
			}
			makers, ds := check.Check("makers", []*syntax.File{lib}, nil)
			if len(ds) != 0 {
				t.Fatal(ds)
			}
			src := "import makers\nfunc main() { with makers.W { makers.A.Use() } }\n"
			file, err := syntax.Parse(src)
			if err != nil {
				t.Fatal(err)
			}
			pkg, ds := check.Check("main", []*syntax.File{file}, contextImporter{makers})
			if len(ds) != 0 {
				t.Fatalf("fresh imported wire refused by the checker: %v", ds)
			}
			for _, w := range pkg.Info.Withs {
				if len(w.Contexts) != 1 || len(w.Acquired) != 1 {
					t.Fatalf("the checker no longer records the imported wire's entry: %+v", w)
				}
			}
			var refused *Refused
			func() {
				defer func() {
					if p := recover(); p != nil {
						t.Fatalf("lowering panicked: %v", p)
					}
				}()
				u := Unit{Package: pkg, Files: []*syntax.File{file}, Sources: []Source{{Path: testSourcePath, Text: src}}}
				_, err = lowerPackage(u, false)
			}()
			if !errors.As(err, &refused) || len(refused.Refusals) != 1 {
				t.Fatalf("want one located refusal, got %v", err)
			}
			r := refused.Refusals[0]
			if r.Line != 2 || r.Column != 20 || !strings.Contains(r.Message, "a with of an imported wire is lowered after check-v1 stage B") {
				t.Fatalf("refusal %d:%d %q, want 2:20 naming stage B", r.Line, r.Column, r.Message)
			}
		})
	}
}
