package check_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"math/rand/v2"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/gobind"
	"ergo.local/prototype/internal/gofacts"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/std/gostd"
	"ergo.local/prototype/internal/sketch/syntax"
	"ergo.local/prototype/internal/toolchain"
	v1 "ergo.local/prototype/runtime/v1"
)

// shapeField is one field of a generated Go struct: its form and the
// named type it holds.
type shapeField struct {
	form   string // "T", "*T", "[]T", "[2]T", "map[string]T", "embed", "*embed"
	target string // T0…, M, A or I (a Go interface; forms T, []T and map[string]T)
}

// shapeModel is a generated Go package of structs T0…Tn-1, M and A. M and
// A have a memory-only method (a planted gofacts.Effects row with a
// validated guard line); A is listed in v1.GoConcurrentTypes(); some Ti have
// a memory-only method of their own, some a foreign one (no row), and the
// rest none. I is a Go interface, which some fields hold.
type shapeModel struct {
	n       int
	fields  [][]shapeField
	planted []bool // Ti has a memory-only row
	plain   []bool // Ti has a method with no row: it touches the host
}

var shapeForms = []string{"T", "*T", "[]T", "[2]T", "map[string]T", "embed", "*embed"}

func newShapeModel(r *rand.Rand) *shapeModel {
	m := &shapeModel{n: 3 + r.IntN(4)}
	m.fields = make([][]shapeField, m.n)
	m.planted = make([]bool, m.n)
	m.plain = make([]bool, m.n)
	for i := range m.n {
		m.planted[i] = r.IntN(6) == 0
		m.plain[i] = !m.planted[i] && r.IntN(4) == 0
		embedded := map[string]bool{}
		for range r.IntN(4) {
			form := shapeForms[r.IntN(len(shapeForms))]
			// A by-value form holds only a later type, M or A: Go has no
			// value cycle.
			byValue := form == "T" || form == "[2]T" || form == "embed"
			var targets []string
			for j := range m.n {
				if !byValue || j > i {
					targets = append(targets, "T"+strconv.Itoa(j))
				}
			}
			targets = append(targets, "M", "A")
			target := targets[r.IntN(len(targets))]
			if (form == "T" || form == "[]T" || form == "map[string]T") && r.IntN(5) == 0 {
				target = "I" // a Go interface: its methods are foreign
			}
			if strings.HasSuffix(form, "embed") {
				if embedded[target] {
					continue
				}
				embedded[target] = true
			}
			m.fields[i] = append(m.fields[i], shapeField{form, target})
		}
	}
	return m
}

// confined is the reference: the type named t reaches, through any field
// of any form, a type with a memory-only method that A's listing does not
// exempt.
func (m *shapeModel) confined(t string, seen map[string]bool) bool {
	if seen[t] {
		return false
	}
	seen[t] = true
	switch t {
	case "M":
		return true
	case "A", "I":
		return false
	}
	i, _ := strconv.Atoi(t[1:])
	if m.planted[i] {
		return true
	}
	for _, f := range m.fields[i] {
		if m.confined(f.target, seen) {
			return true
		}
	}
	return false
}

// touches is GBX1b's reference: the type named t reaches, through any
// field of any form, a type with a method that has no memory-only row: its
// own or a Go interface's. A method Go promotes from an embedded field
// takes the row of the type that declares it (GB; interop.md, Purity and
// authority), and the walk reaches that type through the embedded field,
// so a promotion adds nothing of its own. M and A have only memory-only
// methods, and A's listing stops nothing here.
func (m *shapeModel) touches(t string, seen map[string]bool) bool {
	if seen[t] || t == "M" || t == "A" {
		return false
	}
	if t == "I" {
		return true
	}
	seen[t] = true
	i, _ := strconv.Atoi(t[1:])
	if m.plain[i] {
		return true
	}
	for _, f := range m.fields[i] {
		if m.touches(f.target, seen) {
			return true
		}
	}
	return false
}

func (m *shapeModel) goSource() string {
	var b strings.Builder
	b.WriteString("package shapes\n\ntype M struct{ s uint64 }\n\nfunc (m *M) Step() uint64 { m.s++; return m.s }\n\ntype A struct{ s uint64 }\n\nfunc (a *A) Step() uint64 { return a.s }\n\ntype I interface{ Step() int }\n")
	for i, fs := range m.fields {
		fmt.Fprintf(&b, "\ntype T%d struct {\n\tN int\n", i)
		for k, f := range fs {
			switch f.form {
			case "embed":
				fmt.Fprintf(&b, "\t%s\n", f.target)
			case "*embed":
				fmt.Fprintf(&b, "\t*%s\n", f.target)
			default:
				fmt.Fprintf(&b, "\tF%d %s\n", k, strings.Replace(f.form, "T", f.target, 1))
			}
		}
		b.WriteString("}\n")
		if m.planted[i] || m.plain[i] {
			fmt.Fprintf(&b, "\nfunc (t *T%d) Step() int { return t.N }\n", i)
		}
	}
	return b.String()
}

// newSource makes each Ti with go.New in a function with no row; want is
// the lines the reference charges go.Foreign at.
func (m *shapeModel) newSource() (string, map[int]string) {
	var b strings.Builder
	b.WriteString("import go shapes\n\n")
	want := map[int]string{}
	for i := range m.n {
		fmt.Fprintf(&b, "func new%d() go.Ref[shapes.T%d] {\n\tgo.New[shapes.T%d]()\n}\n\n", i, i, i)
		if m.touches("T"+strconv.Itoa(i), map[string]bool{}) {
			want[4+4*i] = "requires go.Foreign"
		}
	}
	return b.String(), want
}

// ergoSource captures a go.Ref to each Ti in a task body and makes a
// queue of each Ti by value; want is the lines the reference refuses.
func (m *shapeModel) ergoSource() (string, map[int]string) {
	var b strings.Builder
	b.WriteString("import task\nimport queue\nimport go shapes\n\nfunc main() needs go.Foreign {\n")
	line := 6
	want := map[int]string{}
	for i := range m.n {
		confined := m.confined("T"+strconv.Itoa(i), map[string]bool{})
		fmt.Fprintf(&b, "\tlet r%d = go.New[shapes.T%d]()\n", i, i)
		fmt.Fprintf(&b, "\tlet w%d = task.Go() { let _ = r%d }\n", i, i)
		if confined {
			want[line+1] = fmt.Sprintf("r%d holds go.Ref[shapes.T%d], which reaches a Go value", i, i)
		}
		fmt.Fprintf(&b, "\tw%d.Wait()\n", i)
		fmt.Fprintf(&b, "\tlet q%d = queue.New[shapes.T%d](1)\n", i, i)
		if confined {
			want[line+3] = "a type argument may carry it to another task"
		}
		fmt.Fprintf(&b, "\tlet _ = q%d\n", i)
		line += 5
	}
	b.WriteString("}\n")
	return b.String(), want
}

// TestGoConfinementReachesThroughShapes (GB-RACE2): a Go type is confined
// to one task when any field, element, Map value, pointer or embedded type
// reachable through its structure has a memory-only method that
// v1.GoConcurrentTypes() does not list. Each seed generates a Go package of
// structs, binds it with ergo bind's own generator, and checks a program
// that captures a go.Ref to each struct in a task and makes a queue of
// each; the refused lines must be the reference reachability model's.
// ERGO_SHAPE_SEED=N replays one seed.
func TestGoConfinementReachesThroughShapes(t *testing.T) {
	checkShapes(t, "TestGoConfinementReachesThroughShapes", "S108", (*shapeModel).ergoSource, (*shapeModel).confined)
}

// TestGoNewChargesOnlyHostTouching (GBX1b): go.New of a Go type charges
// go.Foreign exactly when the type, through the structure GB-RACE2's walk
// follows, reaches a method with no memory-only row; a type whose
// reachable methods are all memory-only, or that has none, is made in a
// frame with no row. Each seed binds the same generated shapes and makes
// each struct with go.New in a function with no row; the S102 lines must
// be the reference model's. ERGO_SHAPE_SEED=N replays one seed.
func TestGoNewChargesOnlyHostTouching(t *testing.T) {
	checkShapes(t, "TestGoNewChargesOnlyHostTouching", "S102", (*shapeModel).newSource, (*shapeModel).touches)
}

// checkShapes runs the seeded shape property: for each seed it plants the
// memory-only rows (gofacts.Effects, each with a validated guard line in
// the ledger the checker reads) and A's listing, binds the generated Go
// package, checks program's Ergo source and compares the lines with a
// diagnostic of code with program's want. It fails when the seeds reach
// one answer of model only.
func checkShapes(t *testing.T, name, code string, program func(*shapeModel) (string, map[int]string), model func(*shapeModel, string, map[string]bool) bool) {
	seeds := []uint64{}
	if s := os.Getenv("ERGO_SHAPE_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seeds = append(seeds, n)
	} else {
		for i := uint64(1); i <= 60; i++ {
			seeds = append(seeds, i)
		}
	}
	savedEffects := gofacts.Effects
	// GoConcurrentTypes is built once per process (ERGO-STARTUP-2): plant
	// the entry in it and take it out again.
	concurrent := v1.GoConcurrentTypes()
	savedPlanted, hadPlanted := concurrent["shapes.A"]
	savedLedger := check.FactLedger
	t.Cleanup(func() {
		gofacts.Effects, check.FactLedger = savedEffects, savedLedger
		if hadPlanted {
			concurrent["shapes.A"] = savedPlanted
		} else {
			delete(concurrent, "shapes.A")
		}
	})
	concurrent["shapes.A"] = "planted: safe for concurrent use"
	yes, no := 0, 0
	for _, seed := range seeds {
		m := newShapeModel(rand.New(rand.NewPCG(seed, 0x6b2ace)))
		planted := []gofacts.Effect{{Target: "shapes.M.Step", Reason: "planted"}, {Target: "shapes.A.Step", Reason: "planted"}}
		for i, p := range m.planted {
			if p {
				planted = append(planted, gofacts.Effect{Target: fmt.Sprintf("shapes.T%d.Step", i), Reason: "planted"})
			}
		}
		gofacts.Effects = append(slices.Clone(savedEffects), planted...)
		ledger := savedLedger()
		for _, e := range planted {
			ledger += "\n" + gofacts.Entry{Key: gofacts.EffectKey(e.Target), Verdict: "validated", By: "planted", With: toolchain.Version, Digest: e.Digest(toolchain.Version)}.Line() + "\n"
		}
		check.FactLedger = func() string { return ledger }
		goSrc := m.goSource()
		binding := bindShapes(t, goSrc)
		if strings.Contains(binding, "ergo:refused") {
			t.Fatalf("seed %d: the binding refuses a generated member; the generator must make only shapes ergo bind maps:\n%s", seed, binding)
		}
		src, want := program(m)
		f, err := syntax.Parse(src)
		if err != nil {
			t.Fatalf("seed %d: parse: %v\n%s", seed, err, src)
		}
		_, ds := check.Check("main", []*syntax.File{f}, drvImporter{check.NewGoBindings(func(path string) (string, bool) {
			if path == "shapes" {
				return binding, true
			}
			return gostd.Binding(path)
		})})
		got := map[int]string{}
		bad := false
		for _, d := range ds {
			w, ok := want[d.Line]
			if d.Code != code || !ok || !strings.Contains(d.Message, w) {
				bad = true
			}
			got[d.Line] = d.Message
		}
		if bad || len(got) != len(want) {
			t.Fatalf("seed %d: %s lines %v, want %v\ndiagnostics: %v\nreplay: ERGO_SHAPE_SEED=%d go test -run '^%s$' ./internal/sketch/check\n--- shapes.go\n%s\n--- main.ergo\n%s", seed, code, keysOf(got), keysOf(want), ds, seed, name, goSrc, src)
		}
		for i := range m.n {
			if model(m, "T"+strconv.Itoa(i), map[string]bool{}) {
				yes++
			} else {
				no++
			}
		}
	}
	if len(seeds) > 1 && (yes == 0 || no == 0) {
		t.Fatalf("the seeds reach one answer only: %d yes, %d no", yes, no)
	}
}

func keysOf(m map[int]string) []int {
	var out []int
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// bindShapes binds the generated package with ergo bind's own generator.
func bindShapes(t *testing.T, src string) string {
	t.Helper()
	fs := token.NewFileSet()
	f, err := parser.ParseFile(fs, "shapes.go", src, 0)
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, src)
	}
	pkg, err := (&types.Config{IgnoreFuncBodies: true}).Check("shapes", fs, []*ast.File{f}, nil)
	if err != nil {
		t.Fatalf("types: %v\n%s", err, src)
	}
	binding, err := gobind.File([]*types.Package{pkg}, []gobind.Platform{{GOOS: "linux", GOARCH: "amd64"}}, "go1.27")
	if err != nil {
		t.Fatal(err)
	}
	return binding
}
