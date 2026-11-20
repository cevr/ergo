package sketchfront

import (
	"flag"
	"fmt"
	"math/rand/v2"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"ergo.local/prototype/internal/gobind"
	"ergo.local/prototype/internal/sketch/check"
)

var unfedSeed = flag.Int64("unfed.seed", -1, "TestUnfedLawIsJudgedOverTheModule: replay this seed alone")

// TestUnfedLawIsJudgedOverTheModule is the module rule of LAW-UNFED
// (types.md, Laws 10), a seeded property over generated two-package
// modules against a reference. Package dom declares a contract with a law
// and providers whose values have no generator; each provider is fed by a
// named context of dom, of mem (which imports dom), of both or of
// neither. The reference: the shared load of the whole module
// (CheckPackages) reports exactly the providers no context feeds, at
// their implements entries, and no other diagnostic; and in every load,
// the shared one and each package checked alone, no package's law runs
// hold a binding of a provider with no generator, so `ergo test` of any
// package neither runs nor skips it (the contexts' runs are the law's).
// So a package's test verdict, and the test cache's key, which holds the
// package and its imports, do not depend on the contexts of the packages
// that import it: adding or removing a feeding context in mem leaves
// dom's runs as they are. A failing seed prints both sources and the
// replay command. Mutants lu-fed-by-importing-package-ignored (a context
// of an importing package feeds nothing), lu-module-unjudged (the shared
// load makes no judgment), lu-deferred-binding-kept (a binding left to
// the module stays a skip) and lu-unprovided-unreported (with no provider
// of Shape at all, its law is no error) are its kills.
func TestUnfedLawIsJudgedOverTheModule(t *testing.T) {
	seeds := []int64{}
	if *unfedSeed >= 0 {
		seeds = append(seeds, *unfedSeed)
	} else {
		for s := range int64(12) {
			seeds = append(seeds, s)
		}
	}
	for _, seed := range seeds {
		g := genUnfedModule(seed)
		if err := g.check(t); err != "" {
			t.Fatalf("seed %d: %s\n== dom/dom.ergo\n%s== mem/mem.ergo\n%s\nreplay: go test ./internal/sketchfront -run TestUnfedLawIsJudgedOverTheModule -unfed.seed=%d", seed, err, g.dom, g.mem, seed)
		}
	}
}

// unfedModule is one generated module: its two sources and the reference
// verdict, the providers no context feeds with their implements entries.
type unfedModule struct {
	dom, mem string
	unfed    map[string]string // provider -> "LINE:COLUMN" of its implements entry

	// unprovided: no provider of Shape, so its law is unfed at the law.
	unprovided bool
}

// genUnfedModule draws zero to four providers, each fed by dom, by mem,
// by both or by neither.
func genUnfedModule(seed int64) unfedModule {
	r := rand.New(rand.NewPCG(uint64(seed), 7))
	var dom, mem strings.Builder
	dom.WriteString("type Shape {\n\tArea() int,\n\tlaw Positive() { Area() > 0 },\n}\n")
	mem.WriteString("import m/dom\n")
	var domCtx, memCtx []string
	g := unfedModule{unfed: map[string]string{}}
	line := 5
	n := r.IntN(5)
	g.unprovided = n == 0
	for i := range n {
		name := fmt.Sprintf("Fn%d", i)
		decl := "type " + name + " { F func(int) int } implements "
		fmt.Fprintf(&dom, "%sShape\nfunc Area(s %s) int { s.F(1) }\n", decl, name)
		value := name + "{F: func(n int) int { n + 1 }}"
		inDom, inMem := r.IntN(2) == 0, r.IntN(2) == 0
		if inDom {
			domCtx = append(domCtx, fmt.Sprintf("let in%s = context { Shape: %s }\n", name, value))
		}
		if inMem {
			memCtx = append(memCtx, fmt.Sprintf("let in%s = context { dom.Shape: dom.%s }\n", name, value))
		}
		if !inDom && !inMem {
			g.unfed[name] = fmt.Sprintf("%d:%d", line, len(decl)+1)
		}
		line += 2
	}
	for _, c := range domCtx {
		dom.WriteString(c)
	}
	for _, c := range memCtx {
		mem.WriteString(c)
	}
	if n > 0 {
		mem.WriteString("func Size(s dom.Fn0) int { s.F(1) }\n")
	} else {
		mem.WriteString("func Size(s dom.Shape) int { s.Area() }\n")
	}
	g.dom, g.mem = dom.String(), mem.String()
	return g
}

// check runs the three loads and returns how they differ from the
// reference, "" when they agree.
func (g unfedModule) check(t *testing.T) string {
	root := writeTree(t, map[string]string{"ergo.mod": "module m\n", "dom/dom.ergo": g.dom, "mem/mem.ergo": g.mem})
	dirs := []string{filepath.Join(root, "dom"), filepath.Join(root, "mem")}
	shared, err := CheckPackages(dirs, gobind.Platform{})
	if err != nil {
		return "CheckPackages: " + err.Error()
	}
	var got, want []string
	for _, d := range shared[0].Diagnostics {
		got = append(got, fmt.Sprintf("%d:%d %s %s", d.Span.Line, d.Span.Column, d.Code, d.Message))
	}
	for name, at := range g.unfed {
		want = append(want, fmt.Sprintf("%s S108 law Shape.Positive, declared at dom.ergo:3:6, cannot run at %s: its values have no generator: func(int) int is a function type; bind one in a named context of a test file, let Name = context { Shape: … }, and the law runs under it; no named context of a package of module m builds one (types.md, Laws 10)", at, name))
	}
	if g.unprovided {
		want = append(want, "3:6 S108 law Shape.Positive cannot run: no package of module m provides Shape, so no case runs it; implement it in a package of the module, or bind a provider in a named context (types.md, Laws 10)")
	}
	sort.Strings(got)
	sort.Strings(want)
	if !slices.Equal(got, want) {
		return fmt.Sprintf("the shared load's diagnostics of dom:\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if len(shared[1].Diagnostics) > 0 {
		return fmt.Sprintf("the shared load's diagnostics of mem: %v", shared[1].Diagnostics)
	}
	loads := []struct {
		name string
		pkg  *check.Package
	}{{"dom in the shared load", shared[0].Root().Package}, {"mem in the shared load", shared[1].Root().Package}}
	for i, dir := range dirs {
		alone, err := CheckDir(dir)
		if err != nil {
			return "CheckDir: " + err.Error()
		}
		if len(alone.Diagnostics) > 0 {
			return fmt.Sprintf("%s alone: %v; a load of one package cannot see the packages that import it", filepath.Base(dir), alone.Diagnostics)
		}
		loads = append(loads, struct {
			name string
			pkg  *check.Package
		}{filepath.Base(dirs[i]) + " alone", alone.Root().Package})
	}
	for _, l := range loads {
		for _, r := range l.pkg.LawRuns {
			for _, b := range r.Bindings {
				for _, p := range b {
					if p.Type != nil && strings.HasPrefix(p.Type.Obj.Name, "Fn") {
						return fmt.Sprintf("%s runs %s at %s (unrun: %q); its law runs under the contexts, or it is the shared load's error", l.name, r.Law.Title(l.pkg), p.Type, p.Unrun)
					}
				}
			}
		}
	}
	return ""
}
