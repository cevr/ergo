package lower

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"math/rand/v2"
	"path"
	"strconv"
	"strings"
	"testing"
)

// TestSitesStayInTheirPackage checks the split's files, not a digest of a
// selected call: a body edit changes just its package, and changing the
// set of packages leaves the unchanged packages' Go and IR byte-equal.
func TestSitesStayInTheirPackage(t *testing.T) {
	for _, seed := range []uint64{1, 7, 42} {
		t.Run(strconv.FormatUint(seed, 10), func(t *testing.T) {
			const aDir = splitDir + "/sites/alpha"
			const bDir = splitDir + "/sites/bravo"
			const cDir = splitDir + "/extra/alpha"
			program := func(edit, add bool) splitProgram {
				r := rand.New(rand.NewPCG(seed, 0x5173))
				p := splitProgram{root: `import sites/alpha
import sites/bravo

func main() needs Console {
	let n = alpha.First() + bravo.Second()
	Console.Println("\(n)")
}
`}
				if add {
					p.root = strings.Replace(p.root, "import sites/bravo\n", "import sites/bravo\nimport extra extra/alpha\n", 1)
					p.root = strings.Replace(p.root, "bravo.Second()", "bravo.Second() + extra.Added()", 1)
					p.libs = append(p.libs, pkgSource{"extra/alpha", `func addedAt(s CallSite) int { s.Pkg + s.Id }
func Added() int { addedAt() }`})
				}
				prefix := ""
				if edit {
					// A new hidden site before the old site moves the old
					// local Id while keeping the same source file and package.
					prefix = "alphaAt() + "
				}
				p.libs = append(p.libs,
					pkgSource{"sites/alpha", fmt.Sprintf("func alphaAt(s CallSite) int { s.Pkg + s.Id + %d }\nfunc First() int { %salphaAt() }", r.IntN(10), prefix)},
					pkgSource{"sites/bravo", fmt.Sprintf("func bravoAt(s CallSite) int { s.Pkg + s.Id + %d }\nfunc Second() int { bravoAt() }", r.IntN(10))},
				)
				return p
			}
			lower := func(p splitProgram) map[string][]byte {
				fs, _, rep, err := LowerIRSplit(p.check(t), false)
				if err != nil || rep.Reason != "" {
					t.Fatalf("split failed: %v; %+v\n%+v", err, rep, p)
				}
				out := make(map[string][]byte, len(fs))
				for _, f := range fs {
					out[f.Path] = f.Data
				}
				return out
			}
			base := lower(program(false, false))
			edited := lower(program(true, false))
			added := lower(program(false, true))
			equalExcept := func(want, got map[string][]byte, p splitProgram, skip func(string) bool) {
				t.Helper()
				for name, data := range got {
					if skip(path.Dir(name)) {
						continue
					}
					old := want[name]
					if name == "main.go" {
						// The existing program-wide trace table changes when
						// any package adds a call. The root's code and IR do not.
						old, data = withoutRootTraceCalls(t, old), withoutRootTraceCalls(t, data)
					}
					if !bytes.Equal(old, data) {
						t.Errorf("%s changed\nprogram: %+v\nbase:\n%s\nnew:\n%s", name, p, old, data)
					}
				}
				for name := range want {
					if _, ok := got[name]; !ok && !skip(path.Dir(name)) {
						t.Errorf("%s disappeared\nprogram: %+v", name, p)
					}
				}
			}
			aIR, bIR, cIR := path.Join(aDir, IRFileName), path.Join(bDir, IRFileName), path.Join(cDir, IRFileName)
			binding := path.Join(sharedDir, "ergoshared.go")
			if len(base[aIR]) == 0 || len(base[bIR]) == 0 || len(added[cIR]) == 0 || bytes.Equal(base[aIR], edited[aIR]) {
				t.Fatal("fixture must lower both original sites and the added site, and edit alpha's IR")
			}
			if !hasSiteOrdinal(base, sharedDir, "sites_salpha", 1) || !hasSiteOrdinal(added, sharedDir, "sites_salpha", 2) {
				t.Fatalf("fixture must change alpha's ordinal in the shared package (binding directory %s)\nbase: %s\nadd: %s", sharedDir, base[binding], added[binding])
			}
			// A second declaration with the same prefix is ordinary root
			// code. Changing it must still be detected.
			a := bytes.Join([][]byte{base["main.go"], []byte("\nvar ergoCallsOther = 1\n")}, nil)
			b := bytes.Join([][]byte{edited["main.go"], []byte("\nvar ergoCallsOther = 2\n")}, nil)
			if bytes.Equal(withoutRootTraceCalls(t, a), withoutRootTraceCalls(t, b)) {
				t.Fatal("filter hid a second changed declaration beside ergoCalls")
			}
			equalExcept(base, edited, program(true, false), func(dir string) bool { return dir == aDir })
			equalExcept(base, added, program(false, true), func(dir string) bool { return dir == "." || dir == sharedDir || dir == cDir })
		})
	}
}

func withoutRootTraceCalls(t *testing.T, src []byte) []byte {
	t.Helper()
	fs := token.NewFileSet()
	f, err := parser.ParseFile(fs, "main.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	file := fs.File(f.Pos())
	var out []byte
	start := 0
	for _, d := range f.Decls {
		g, ok := d.(*ast.GenDecl)
		if !ok || g.Tok != token.VAR || g.Lparen.IsValid() || len(g.Specs) != 1 {
			continue
		}
		spec := g.Specs[0].(*ast.ValueSpec)
		if len(spec.Names) != 1 || spec.Names[0].Name != "ergoCalls" {
			continue
		}
		out = append(out, src[start:file.Offset(g.Pos())]...)
		start = file.Offset(g.End())
	}
	if start == 0 {
		t.Fatal("fixture has no root trace call table")
	}
	return append(out, src[start:]...)
}

func hasSiteOrdinal(fs map[string][]byte, dir, alias string, ordinal int) bool {
	binding := fmt.Appendf(nil, "%s = %d\n", HelperSpelling("", "SitePkg_src__"+alias), ordinal)
	for name, data := range fs {
		if path.Dir(name) == dir && bytes.Contains(data, binding) {
			return true
		}
	}
	return false
}
