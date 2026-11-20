package sketchfront

import (
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/gobind"
)

// TestUnfedLawIsJudgedOverTheModule is the module rule of LAW-UNFED
// (types.md, Laws 10): a contract's law is fed when a named context of any
// package of its module builds the provider, so a provider of the domain
// package that only the memory package builds is no error of a shared load
// of the module, and its run is the context's. With no such context the
// shared load reports it at the provider; a load of the domain package
// alone cannot see its importers and reports nothing. Mutants
// fed-by-importing-package-ignored (a context of an importing package
// feeds nothing) and lu-module-unjudged (the shared load makes no
// judgment) are its kills.
func TestUnfedLawIsJudgedOverTheModule(t *testing.T) {
	const dom = `type Shape {
	Area() int,
	law Positive() { Area() > 0 },
}
type Fn { F func(int) int } implements Shape
func Area(s Fn) int { s.F(1) }
`
	const fed = `import m/dom
let overFn = context { dom.Shape: dom.Fn{F: func(n int) int { n + 1 }} }
func Size() int { 1 }
`
	const unfed = `import m/dom
func Size(s dom.Fn) int { s.F(1) }
`
	rows := []struct {
		name, mem string
		want      string // the domain package's one diagnostic; "" for none
	}{
		{name: "fed by the memory package", mem: fed},
		{name: "fed by no package", mem: unfed, want: "S108 law Shape.Positive, declared at dom.ergo:3:6, cannot run at Fn: its values have no generator: func(int) int is a function type; bind one in a named context of a test file, let Name = context { Shape: … }, and the law runs under it; no named context of a package of module m builds one (types.md, Laws 10)"},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			root := writeTree(t, map[string]string{
				"ergo.mod":     "module m\n",
				"dom/dom.ergo": dom,
				"mem/mem.ergo": row.mem,
			})
			dirs := []string{filepath.Join(root, "dom"), filepath.Join(root, "mem")}
			as, err := CheckPackages(dirs, gobind.Platform{})
			if err != nil {
				t.Fatalf("CheckPackages: %v", err)
			}
			var got []string
			for _, d := range as[0].Diagnostics {
				got = append(got, d.Code+" "+d.Message)
			}
			if len(as[1].Diagnostics) > 0 {
				t.Errorf("the memory package: %v", as[1].Diagnostics)
			}
			switch {
			case row.want == "" && len(got) > 0:
				t.Fatalf("the domain package: %s, want none", strings.Join(got, "\n"))
			case row.want != "" && (len(got) != 1 || got[0] != row.want):
				t.Fatalf("the domain package:\n%s\nwant\n%s", strings.Join(got, "\n"), row.want)
			}
			if row.want == "" {
				for _, r := range as[0].Root().Package.LawRuns {
					if len(r.Bindings) > 0 {
						t.Errorf("the domain package still binds %s at %s; its run is the memory package's context's", r.Law.Title(as[0].Root().Package), r.Bindings[0][0].Type)
					}
				}
			}
			alone, err := CheckDir(dirs[0])
			if err != nil {
				t.Fatalf("CheckDir: %v", err)
			}
			if len(alone.Diagnostics) > 0 {
				t.Errorf("the domain package alone: %v; a load of it cannot see the packages that import it", alone.Diagnostics)
			}
		})
	}
}
