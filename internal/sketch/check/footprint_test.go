package check_test

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// #199 stage 1: Info.Footprints is what a frame runs, not its row. A with
// discharges a row entry and leaves the footprint; an empty row is no
// purity; a recursive group joins to its least fixpoint; a literal's body
// counts where the literal runs, never where it is written.
func TestFootprints(t *testing.T) {
	src := `import task
extern go strings.TrimSpace func hostTrim(s string) string
type Probe { Tick() }
type Silent {}
func Tick(_ Silent) {}
func loud() Silent {
	let _ = hostTrim("")
	Silent
}
let Local = context { Probe: Silent }
let Loud = context { Probe: loud() }
func pure(a int) int { a + 1 }
func pureCall() int { pure(1) }
func capCall() needs Probe { Probe.Tick() }
func trans() needs Probe { capCall() }
func ownWith() { with Local { Probe.Tick() } }
func loudWith() { with Loud { Probe.Tick() } }
func even(n int) bool {
	if n == 0 {
		return true
	}
	odd(n - 1)
}
func odd(n int) bool {
	if n == 0 {
		return false
	}
	even(n - 1)
}
func ping(n int) needs Probe {
	if n > 0 {
		pong(n - 1)
	}
}
func pong(n int) needs Probe {
	Probe.Tick()
	ping(n)
}
func value(cb func()) { cb() }
func defines() func() needs Probe { func() { Probe.Tick() } }
func immediate() needs Probe { func() { Probe.Tick() }() }
func starts() {
	let t = task.Go() { pure(1) }
	t.Stop()
}
func host(s string) string { hostTrim(s) }
func seqLoop(s Seq[int]) int {
	let mut n = 0
	for v in s {
		n += v
	}
	n
}
func rangeLoop() int {
	let mut n = 0
	for i in 0..3 {
		n += i
	}
	n
}
let seed = pure(1)
let started = pureCall()
`
	file, err := syntax.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	pkg, diags := check.Check("main", []*syntax.File{file}, nil)
	if len(diags) > 0 {
		t.Fatalf("diagnostics: %v", diags)
	}
	fps := pkg.Info.Footprints
	got := map[string]string{}
	for _, f := range pkg.Funcs {
		if f.Decl != nil && f.Decl.Body != nil {
			fp, ok := fps[f.Decl]
			if !ok {
				t.Errorf("func %s has no footprint", f.Name)
				continue
			}
			got["func "+f.Name] = show(fp)
		}
	}
	lits := 0
	for _, d := range file.Decls {
		switch d := d.(type) {
		case *syntax.ContextDecl:
			fp, ok := fps[d]
			if !ok {
				t.Errorf("context %s has no footprint", d.Name.Name)
			}
			got["context "+d.Name.Name] = show(fp)
		case *syntax.VarDecl:
			for _, s := range d.Specs {
				fp, ok := fps[s]
				if !ok {
					t.Errorf("let %s has no footprint", s.Names[0].Name)
				}
				got["let "+s.Names[0].Name] = show(fp)
			}
		case *syntax.FuncDecl:
			syntax.Inspect(d, func(n syntax.Node) bool {
				switch n := n.(type) {
				case *syntax.FuncLit:
					lits++
					got[fmt.Sprintf("literal in %s", d.Name.Name)] = show(fps[n])
				case *syntax.CallExpr:
					// A with block runs in its frame; any other trailing
					// block is a frame of its own.
					if n.Trailing != nil {
						_, ok := fps[n.Trailing]
						if with := pkg.Info.Calls[n].Kind == check.CallWith; ok == with {
							t.Errorf("the trailing block in %s (with %v) has a footprint: %v", d.Name.Name, with, ok)
						}
						if ok {
							got[fmt.Sprintf("block in %s", d.Name.Name)] = show(fps[n.Trailing])
						}
					}
				}
				return true
			})
		}
	}
	want := map[string]string{
		"func Tick":            "pure",
		"func loud":            "pure", // a pure-tier user extern is transparent (IG4, q35)
		"context Local":        "pure",
		"context Loud":         "pure",
		"func pure":            "pure",
		"func pureCall":        "pure",
		"func capCall":         "Probe",
		"func trans":           "Probe",
		"func ownWith":         "Probe",
		"func loudWith":        "Probe",
		"func even":            "pure",
		"func odd":             "pure",
		"func ping":            "Probe",
		"func pong":            "Probe",
		"func value":           "opaque",
		"func defines":         "pure",
		"literal in defines":   "Probe",
		"func immediate":       "Probe",
		"literal in immediate": "Probe",
		"func starts":          "opaque tasks",
		"block in starts":      "pure",
		"func host":            "pure",
		"func seqLoop":         "opaque",
		"func rangeLoop":       "pure",
		"let seed":             "pure",
		"let started":          "pure",
	}
	if lits != 2 {
		t.Errorf("saw %d literals, want 2", lits)
	}
	keys := make([]string, 0, len(want))
	for k := range want {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if got[k] != want[k] {
			t.Errorf("%s: footprint %q, want %q", k, got[k], want[k])
		}
	}
	for k := range got {
		if _, ok := want[k]; !ok {
			t.Errorf("%s: unexpected footprint %q", k, got[k])
		}
	}
}

// #199 stage 3c r1 M1: a with's footprint covers the providers its
// With.Acquired fact builds, whatever the context's spelling. Each body's own
// footprint is empty; only its checked needs reach the providers. B's
// provider mk uses A, so acquiring B acquires A too: the named, embedded
// and inline forms have the same footprint. A block that needs only A
// never runs mk: an inline context builds only its acquired entries in the
// frame, so its with is pure; `with W` calls W's builder frame, whose
// footprint is every provider W binds (stage 1), a superset.
func TestFootprintsWithSpellings(t *testing.T) {
	src := `type A { Use() }
type B { Use() }
type PX {}
type PY {}
func Use(_ PX) {}
func Use(_ PY) {}
func mk() PY needs A {
	A.Use()
	PY
}
let W = context { A: PX, B: mk() }
let V = context { W }
func idle() needs B {}
func onlyA() needs A {}
func named() { with W { idle() } }
func embedded() { with context { W } { idle() } }
func nested() { with context { V } { idle() } }
func namedTwice() { with V { idle() } }
func own() { with context { A: PX, B: mk() } { idle() } }
func namedA() { with W { onlyA() } }
func embeddedA() { with context { W } { onlyA() } }
func nestedA() { with context { V } { onlyA() } }
`
	checkFootprints(t, src, map[string]string{
		"idle": "pure", "onlyA": "pure", "mk": "A",
		"named": "A", "embedded": "A", "nested": "A", "namedTwice": "A", "own": "A",
		"namedA": "A", "embeddedA": "pure", "nestedA": "pure",
	})
}

// #199 stage 3c r2 M1: a with also runs, when its block ends, the Close of
// each provider it acquired (Acquisition.Close), in its own frame. PR's
// Close uses A, so each spelling of a with that acquires PR has A in its
// footprint although its block and constructor are pure. A pure Close adds
// nothing, and a provider the block never reaches is neither built nor
// released.
func TestFootprintsWithReleases(t *testing.T) {
	src := `type A { Use() }
type R { Use() }
type Q { Use() }
type PX {}
type PR {}
type PQ {}
func Use(_ PX) {}
func Use(_ PR) {}
func Use(_ PQ) {}
func Close(_ PR) needs A { A.Use() }
func Close(_ PQ) {}
func idle() needs R {}
func idleQ() needs Q {}
func onlyA() needs A {}
let WR = context { R: PR }
let VR = context { WR }
let WQ = context { Q: PQ }
func inline() needs A { with context { R: PR } { idle() } }
func named() needs A { with WR { idle() } }
func embedded() needs A { with context { WR } { idle() } }
func nested() needs A { with context { VR } { idle() } }
func pureClose() { with context { Q: PQ } { idleQ() } }
func pureNamed() { with WQ { idleQ() } }
func unreached() needs A { with context { WR } { onlyA() } }
`
	checkFootprints(t, src, map[string]string{
		"idle": "pure", "idleQ": "pure", "onlyA": "pure",
		"inline": "A", "named": "A", "embedded": "A", "nested": "A",
		"pureClose": "pure", "pureNamed": "pure", "unreached": "pure",
	})
}

// checkFootprints checks src and compares each named function's footprint
// with want.
func checkFootprints(t *testing.T, src string, want map[string]string) {
	t.Helper()
	file, err := syntax.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	pkg, diags := check.Check("main", []*syntax.File{file}, nil)
	if len(diags) > 0 {
		t.Fatalf("diagnostics: %v", diags)
	}
	for _, f := range pkg.Funcs {
		w, ok := want[f.Name]
		if !ok {
			continue
		}
		delete(want, f.Name)
		if got := show(pkg.Info.Footprints[f.Decl]); got != w {
			t.Errorf("%s: footprint %q, want %q", f.Name, got, w)
		}
	}
	for name := range want {
		t.Errorf("func %s not checked", name)
	}
}

// show prints a footprint: its capabilities by name, then its flags, or
// "pure" when it is empty.
func show(fp check.Footprint) string {
	if fp.Empty() {
		return "pure"
	}
	var parts []string
	for _, c := range fp.Caps {
		parts = append(parts, c.(*check.Named).Obj.Name)
	}
	if fp.Opaque {
		parts = append(parts, "opaque")
	}
	if fp.Tasks {
		parts = append(parts, "tasks")
	}
	return strings.Join(parts, " ")
}

// A footprint never follows the row: a function whose inferred row is
// empty still has the footprint of what it runs, and a function whose
// written row names a capability it never exercises is pure.
func TestFootprintsAreNotRows(t *testing.T) {
	src := `type Probe { Tick() }
type Silent {}
func Tick(_ Silent) {}
let Local = context { Probe: Silent }
func wide() needs Probe {}
func hidden() { with Local { Probe.Tick() } }
`
	file, err := syntax.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	pkg, diags := check.Check("main", []*syntax.File{file}, nil)
	if len(diags) > 0 {
		t.Fatalf("diagnostics: %v", diags)
	}
	for _, f := range pkg.Funcs {
		fp := pkg.Info.Footprints[f.Decl]
		switch f.Name {
		case "wide":
			if !fp.Empty() {
				t.Errorf("wide: footprint %q, want pure despite its row", show(fp))
			}
		case "hidden":
			if fp.Empty() || f.Sig.Uses.String() != "" {
				t.Errorf("hidden: footprint %q, row %q; want Probe with an empty row", show(fp), f.Sig.Uses.String())
			}
		}
	}
}
