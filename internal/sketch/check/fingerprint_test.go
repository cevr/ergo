package check

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"sort"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/syntax"
)

// The fingerprint property (CS5a; docs/spec/build.md, Interface
// fingerprint). A three-package program, a ← b ← c, is edited one slot at
// a time: a comment, a body, a signature, an export added or removed, a
// row, an inferred row, a law, a refinement, a constraint, a file added or
// removed, an import added. Each step checks the program from scratch, the
// reference, and asks Reusable which packages the last check could have
// stood for.

// fpSlots are the slots of the program and the number of values of each.
var fpSlots = map[string]int{
	"comment.a": 4, "comment.b": 4, "comment.c": 4, // comment and layout
	"body.a": 4, "body.b": 4, // a body
	"sig":        2, // a's Sig returns int or string
	"extra":      2, // a's Extra is declared or not
	"file":       2, // a's second file is there or not
	"row":        2, // a's Get fails Gone, or Gone | Other
	"helper":     3, // a's helper fails Gone, or Gone | Other in two bodies (an inferred row)
	"law":        3, // the text of a's law Counter.Grows
	"where":      3, // a's Small is 0..9, 0..99 or 0..5 (b's 7 breaks the last)
	"constraint": 2, // a's Same takes comparable or ordered (b's Plus is no ordered)
	"import":     2, // c imports a too, or not
}

// fpSources are the packages' files for the slot values s, by package.
func fpSources(s map[string]int) map[string]map[string]string {
	comment := func(k string) string {
		return fmt.Sprintf("// Package %s, version %d.%s\n", k, s["comment."+k], strings.Repeat("\n", s["comment."+k]))
	}
	pick := func(slot string, vs ...string) string { return vs[s[slot]] }
	a := comment("a") + `
// Gone and Other are refusals.
type Gone { Why string }
type Other { Why string }

// Small is a small count.
type Small int where 0 <= it && it <= ` + pick("where", "9", "99", "5") + `

// Get is n, or a refusal.
func Get(n int) int fails ` + pick("row", "Gone", "Gone | Other") + ` {
	if n < 0 {
		return Gone{Why: "negative"}
	}
	n
}

func helper(n int) int {
	if n < 0 {
		return Gone{Why: "no"}
	}
` + pick("helper", "\tn + 1", "\ttry refuse(n) + 1", "\ttry refuse(n + 1)") + `
}

func refuse(n int) int fails Other {
	if n > 9 {
		return Other{Why: "big"}
	}
	n
}

// Step is helper, with the row the checker infers for it.
let Step = helper

// Counter counts.
type Counter {
	Next(n int) int,

	law Grows(n int) { ` + pick("law", "Next(n) >= n", "Next(n) > n - 1", "Next(n) != n - 1") + ` },
}

// Same is whether x and y are equal.
func Same[T ` + pick("constraint", "comparable", "ordered") + `](x T, y T) bool { x == y }

// Body is n times a constant.
func Body(n int) int { n * ` + fmt.Sprint(2+s["body.a"]) + ` }

// Sig is n.
func Sig(n int) ` + pick("sig", "int { n }", "string { \"\\(n)\" }") + `
` + pick("extra", "", "\n// Extra is one.\nfunc Extra() int { 1 }\n")
	b := comment("b") + `
import a

// Plus counts by one.
type Plus { By int }

// Next is n plus one.
func Next(p Plus, n int) int { n + 1 }

func get() int { try a.Get(1) }

func step() int { try a.Step(1) }

let small a.Small = 7

func same() bool { a.Same(Plus{By: 1}, Plus{By: 2}) }

func sig() string { "\(a.Sig(1))" }

// Sig is a.Sig, with its type.
let Sig = a.Sig

// Fetch is a.Get, with its row.
let Fetch = a.Get

// Body is a's body plus a constant.
func Body(n int) int { a.Body(n) + ` + fmt.Sprint(s["body.b"]) + ` }
`
	c := comment("c") + `
import b
` + pick("import", "", "import a\n") + `
func fetch() int { try b.Fetch(1) }

func body() int { b.Body(2) }

func sig() string { "\(b.Sig(1))" }
` + pick("import", "", "\nfunc direct() int { a.Body(3) }\n")
	out := map[string]map[string]string{"a": {"a.ergo": a}, "b": {"b.ergo": b}, "c": {"c.ergo": c}}
	if s["file"] == 1 {
		out["a"]["two.ergo"] = "// Two is two.\nfunc Two() int { 2 }\n"
	}
	return out
}

// fpPackages are the packages in import order, with their imports.
var fpPackages = []struct {
	path    string
	imports func(s map[string]int) []string
}{
	{"a", func(map[string]int) []string { return nil }},
	{"b", func(map[string]int) []string { return []string{"a"} }},
	{"c", func(s map[string]int) []string {
		if s["import"] == 1 {
			return []string{"a", "b"}
		}
		return []string{"b"}
	}},
}

type fpImporter map[string]*Package

func (m fpImporter) Import(path string) (*Package, error) {
	if p, ok := m[path]; ok {
		return p, nil
	}
	return Std.Import(path)
}

// fpCheck is one check of the program from scratch: each package's
// fingerprints and view (fpView), in import order. A package whose import
// has diagnostics is not checked, as the loader does.
type fpCheck struct {
	fps   []Fingerprint
	views map[string]string
	// sources is the source fingerprint of each package's import closure,
	// the reference for Implementation.
	closure map[string]string
}

func fpRun(t *testing.T, s map[string]int) fpCheck {
	t.Helper()
	srcs := fpSources(s)
	imp := fpImporter{}
	byPath := map[string]Fingerprint{}
	out := fpCheck{views: map[string]string{}, closure: map[string]string{}}
	for _, p := range fpPackages {
		var files []*syntax.File
		names := make([]string, 0, len(srcs[p.path]))
		for name := range srcs[p.path] {
			names = append(names, name)
		}
		slices.Sort(names)
		for _, name := range names {
			f, err := syntax.Parse(srcs[p.path][name])
			if err != nil {
				t.Fatalf("%s/%s does not parse: %v", p.path, name, err)
			}
			f.Name = name
			files = append(files, f)
		}
		var imports []Fingerprint
		broken := false
		closure := []string{SourceFingerprint(p.path, files, "")}
		for _, q := range p.imports(s) {
			imports = append(imports, byPath[q])
			broken = broken || !byPath[q].Clean()
			closure = append(closure, q+"="+out.closure[q])
		}
		slices.Sort(closure)
		out.closure[p.path] = strings.Join(closure, ",")
		var pkg *Package
		var ds []Diagnostic
		if !broken {
			pkg, ds = Check(p.path, files, imp)
			imp[p.path] = pkg
			out.views[p.path] = fpView(pkg, ds)
		}
		fp := FingerprintOf(p.path, p.path, files, pkg, !broken && len(ds) == 0, "", imports)
		byPath[p.path] = fp
		out.fps = append(out.fps, fp)
	}
	return out
}

// fpView is what a package's check gave, written apart from the interface
// text: its diagnostics, the type of each function with its rows, the type
// of every expression, its laws to run with their text, its tests.
func fpView(pkg *Package, ds []Diagnostic) string {
	var b strings.Builder
	for _, d := range ds {
		fmt.Fprintf(&b, "diag %s %s\n", d.Code, d.Message)
	}
	if pkg == nil {
		return b.String()
	}
	for _, f := range pkg.Funcs {
		fmt.Fprintf(&b, "func %s %s\n", f.Name, typeString(f.Sig))
	}
	var lines []string
	for _, t := range pkg.Info.Types {
		lines = append(lines, typeString(t))
	}
	for name, o := range pkg.Scope.names {
		if v, ok := o.(*Var); ok {
			lines = append(lines, "let "+name+" "+typeString(v.Type))
		}
	}
	sort.Strings(lines)
	b.WriteString(strings.Join(lines, "\n"))
	for _, r := range pkg.LawRuns {
		fmt.Fprintf(&b, "\nlaw %s.%s %s", r.Law.Pkg.Path, r.Law.Name, lawText(r.Law))
		for _, bind := range r.Bindings {
			for _, p := range bind {
				fmt.Fprintf(&b, " %s", typeString(p.Type))
			}
		}
	}
	for _, tt := range pkg.Tests {
		fmt.Fprintf(&b, "\ntest %s", tt.Decl.Name.Name)
	}
	return b.String()
}

// lawText is the token text of a law's declaration, found in the file of
// its package that holds it.
func lawText(l *Law) string {
	for _, fi := range l.Pkg.files {
		f := fi.syntax
		var laws []*syntax.LawDecl
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *syntax.LawDecl:
				laws = append(laws, d)
			case *syntax.TypeDecl:
				if it, ok := d.Spec.(*syntax.InterfaceType); ok {
					laws = append(laws, it.Laws...)
				}
			}
		}
		if slices.Contains(laws, l.Decl) && l.Decl.Derived == nil {
			return syntax.TokenText(f.Src[l.Decl.Pos():l.Decl.End()])
		}
	}
	return "derived"
}

// TestIncrementalCheckDecides: over seeded edit sequences, every package
// Reusable names checks to the view and the interface it had (the
// reference is the check from scratch), a comment edit leaves every clean
// package reusable and a body edit every clean package but its own, and
// two checks give one package the same implementation fingerprint exactly
// when its import closure has the same sources. It kills cs-iface-omits-row,
// cs-iface-omits-inferred-row, cs-iface-omits-law, cs-iface-omits-invariant,
// cs-iface-omits-constraint, cs-iface-shallow, cs-eval-key-omits-dep-body
// and cs-fingerprint-reads-position. A failing seed prints its edits.
func TestIncrementalCheckDecides(t *testing.T) {
	slots := make([]string, 0, len(fpSlots))
	for k := range fpSlots {
		slots = append(slots, k)
	}
	slices.Sort(slots)
	for seed := uint64(1); seed <= 8; seed++ {
		r := rand.New(rand.NewPCG(seed, 5))
		state := map[string]int{}
		prev := fpRun(t, state)
		var edits []string
		for step := range 2*len(slots) + 24 {
			// The first steps edit each slot from the first program and
			// then undo the edit, so each seed makes every edit to a clean
			// program; the rest draw a slot and a value.
			slot := slots[r.IntN(len(slots))]
			if step < 2*len(slots) {
				slot = slots[(step/2+int(seed))%len(slots)]
			}
			old := state[slot]
			state[slot] = (old + 1 + r.IntN(fpSlots[slot]-1)) % fpSlots[slot]
			if step < 2*len(slots) && step%2 == 1 {
				state[slot] = 0
			}
			edits = append(edits, fmt.Sprintf("%s=%d", slot, state[slot]))
			cur := fpRun(t, state)
			fail := func(format string, args ...any) {
				t.Helper()
				t.Fatalf("seed %d, edits %s: %s", seed, strings.Join(edits, " "), fmt.Sprintf(format, args...))
			}
			reused := Reusable(prev.fps, cur.fps)
			for i, fp := range cur.fps {
				was := prev.fps[i]
				if slices.Contains(reused, fp.Path) {
					if cur.views[fp.Path] != prev.views[fp.Path] {
						fail("%s is reusable, but its check changed:\n--- before\n%s\n--- after\n%s", fp.Path, prev.views[fp.Path], cur.views[fp.Path])
					}
					if fp.Interface != was.Interface {
						fail("%s is reusable, but its interface changed", fp.Path)
					}
				}
				wantReused := false
				switch {
				case strings.HasPrefix(slot, "comment."):
					wantReused = was.Clean()
				case strings.HasPrefix(slot, "body."):
					wantReused = was.Clean() && slot != "body."+fp.Path
				}
				if wantReused && !slices.Contains(reused, fp.Path) {
					fail("after the %s edit, %s (clean before) is not reusable", slot, fp.Path)
				}
				sameClosure := cur.closure[fp.Path] == prev.closure[fp.Path]
				if sameImpl := fp.Implementation == was.Implementation; sameImpl != sameClosure {
					fail("%s: the same implementation fingerprint is %v, the same closure sources %v", fp.Path, sameImpl, sameClosure)
				}
			}
			prev = cur
		}
	}
}
