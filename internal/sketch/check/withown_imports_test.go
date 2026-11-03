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

func TestContextOwnershipUserExtern(t *testing.T) {
	const src = `extern go example.com/x.Get func get() First
let W = context { A: get() }
func f() { with W { A.Use() } }
`
	// The extern's dotted target is outside the C26 domain: S109 #202 at
	// the declaration (check-v1 stage E4), besides the provider's S109.
	_, ds := checkOwn(t, src)
	decl := strings.Count(ownPrelude, "\n") + 1
	if len(ds) != 2 || ds[0].Code != "S109" || ds[0].Line != decl || !strings.Contains(ds[0].Message, "#202") ||
		ds[1].Code != "S109" || ds[1].Line != decl+1 || !strings.Contains(ds[1].Message, "#197") {
		t.Fatalf("diagnostics %v, want S109 #202 at the extern and a located S109 for an untrusted user extern provider", ds)
	}
}

func TestContextOwnershipImportedReleaseNeeds(t *testing.T) {
	lib, err := syntax.Parse(`type A { Use() }
type Conn { n int }
func Use(_ Conn) {}
func Close(_ Conn) needs Console { Console.Println("closed") }
func Get() Conn { Conn{n: 1} }
let W = context { A: Get() }
`)
	if err != nil {
		t.Fatal(err)
	}
	makers, ds := check.Check("makers", []*syntax.File{lib}, nil)
	if len(ds) != 0 {
		t.Fatal(ds)
	}
	for _, declared := range []bool{false, true} {
		src := "import makers\nfunc f()"
		if declared {
			src += " needs Console"
		}
		src += " { with makers.W { makers.A.Use() } }"
		file, err := syntax.Parse(src)
		if err != nil {
			t.Fatal(err)
		}
		_, ds := check.Check("main", []*syntax.File{file}, ownImporter{makers})
		if declared {
			if len(ds) != 0 {
				t.Fatalf("declared imported release needs refused: %v", ds)
			}
		} else if len(ds) != 1 || ds[0].Code != "S102" || ds[0].Line != 2 || !strings.Contains(ds[0].Message, "makers.Conn") {
			t.Fatalf("want located S102 naming imported provider's type, got %v", ds)
		}
	}
}
