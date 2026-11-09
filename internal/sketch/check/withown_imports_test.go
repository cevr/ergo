package check_test

import (
	"fmt"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

func TestContextOwnershipImportedContext(t *testing.T) {
	for _, fresh := range []bool{false, true} {
		for _, embedded := range []bool{false, true} {
			for _, missing := range []string{"", "call", "type"} {
				t.Run(fmt.Sprintf("fresh=%v/embedded=%v/missing=%s", fresh, embedded, missing), func(t *testing.T) {
					result := "shared"
					if fresh {
						result = "Conn{n: 2}"
					}
					contexts := "let W = context { A: Get() }\n"
					if embedded {
						contexts = "let Base = context { A: Get() }\nlet W = context { Base }\n"
					}
					lib, err := syntax.Parse(`type A { Use() }
type Conn { n int }
func Use(_ Conn) {}
func Close(_ Conn) {}
let shared = Conn{n: 1}
func Get() Conn { ` + result + ` }
` + contexts)
					if err != nil {
						t.Fatal(err)
					}
					makers, ds := check.Check("makers", []*syntax.File{lib}, nil)
					if len(ds) != 0 {
						t.Fatal(ds)
					}
					owner := makers.Contexts["W"]
					if embedded {
						owner = makers.Contexts["Base"]
					}
					provider := owner.Entries[0].Provider
					if missing == "call" {
						delete(makers.Info.Calls, provider.(*syntax.CallExpr))
					}
					if missing == "type" {
						delete(makers.Info.Types, provider)
					}
					src := "import makers\n"
					context := "makers.W"
					line := strings.Count(src, "\n") + 1
					src += "func f() { with " + context + " { makers.A.Use() } }\n"
					main, err := syntax.Parse(src)
					if err != nil {
						t.Fatal(err)
					}
					pkg, ds := check.Check("main", []*syntax.File{main}, ownImporter{makers})
					if !fresh || missing != "" {
						if len(ds) != 1 || ds[0].Code != "S109" || ds[0].Line != line || ds[0].Column != 12 || !strings.Contains(ds[0].Message, "#197") {
							t.Fatalf("diagnostics %v, want one S109 naming #197 at the importing with", ds)
						}
						return
					}
					if len(ds) != 0 {
						t.Fatalf("fresh imported context refused: %v", ds)
					}
					if len(pkg.Info.Withs) != 1 {
						t.Fatal("missing with fact")
					}
					for _, w := range pkg.Info.Withs {
						if len(w.Acquired) != 1 || w.Acquired[0].Close == nil || w.Acquired[0].Close.Func == nil || w.Acquired[0].Close.Func.Pkg != makers {
							t.Fatalf("missing imported acquisition and Close identity: %+v", w.Acquired)
						}
					}
				})
			}
		}
	}
}
