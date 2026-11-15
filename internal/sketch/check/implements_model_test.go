package check_test

import (
	"fmt"
	"math/rand/v2"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// TestDeclaredRelationModel (EXC1 Property 1; types.md Interface
// assignability 1) is a seeded property of the declared relation over a
// module of two packages. Package lib declares one to three operation
// types Op0…, each with one operation of its own, an include of an earlier
// one (so an include reaches depth two) or none, and a member law, plus
// the generic Gen[T]. Package main declares types of every kind (a
// record, a union, a defined type, a generic record) whose functions fit
// each operation, miss it or fail wider, each with a clause naming some of
// the operation types or none, and uses each type at one site of rule 2:
// an argument, a result, a context entry, a constraint. The reference
// model is the set of written clauses closed over the includes: a clause
// is S106 exactly when the functions miss or widen an operation of what
// it names (once per name); a use is refused exactly when the model does
// not hold the pair, S106 at a context entry and S108 elsewhere; and the
// law runs main's test build lists are exactly the model's pairs of a
// non-generic type with an operation type that has a law and whose
// operations the type has (an unmet name elsewhere in the clause does not
// stop it). ERGO_SEED=<n> replays one seed; a failure prints both packages and
// both verdicts.
//
// Mutants it kills that no other test does (ledger: implements.go): the
// method-set match admits an undeclared type at an argument, a result, a
// context entry, a constraint or as a law provider; a clause with a
// missing function is accepted; a clause with a wider failure row is
// accepted; an include is not followed at depth two; a use repeats the
// S106 of an unmet clause; a clause that names a record, or one operation
// type twice, is accepted.
func TestDeclaredRelationModel(t *testing.T) {
	seeds := make([]uint64, 0, 200)
	if s := os.Getenv("ERGO_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatalf("ERGO_SEED: %v", err)
		}
		seeds = append(seeds, n)
	} else {
		for i := range uint64(200) {
			seeds = append(seeds, i+1)
		}
	}
	for _, seed := range seeds {
		m := newRelModel(seed)
		libSrc, mainSrc := m.program()
		lf, err := syntax.Parse(libSrc)
		if err != nil {
			t.Fatalf("seed %d: lib: %v\n%s", seed, err, libSrc)
		}
		lib, ds := check.Check("lib", []*syntax.File{lf}, nil)
		if len(ds) != 0 {
			t.Fatalf("seed %d: lib: %v\n%s", seed, ds, libSrc)
		}
		mf, err := syntax.Parse(mainSrc)
		if err != nil {
			t.Fatalf("seed %d: main: %v\n%s", seed, err, mainSrc)
		}
		pkg, diags := check.Check("main", []*syntax.File{mf}, libImporter{lib})
		var got []string
		for _, d := range diags {
			got = append(got, fmt.Sprintf("%d %s", d.Line, d.Code))
		}
		slices.Sort(got)
		want := slices.Clone(m.want)
		slices.Sort(want)
		var runs []string
		if pkg != nil {
			for _, r := range pkg.LawRuns {
				for _, b := range r.Bindings {
					for _, p := range b {
						runs = append(runs, p.Type.Obj.Name+" "+r.Law.Name)
					}
				}
			}
		}
		slices.Sort(runs)
		wantRuns := slices.Clone(m.runs)
		slices.Sort(wantRuns)
		if !slices.Equal(got, want) || !slices.Equal(runs, wantRuns) {
			var full []string
			for _, d := range diags {
				full = append(full, fmt.Sprintf("%d:%d %s %s", d.Line, d.Column, d.Code, d.Message))
			}
			t.Fatalf("seed %d (replay: ERGO_SEED=%d go test -run TestDeclaredRelationModel ./internal/sketch/check)\nreference: %v\nchecker:   %v\nreference law runs: %v\nchecker law runs:   %v\n%s\n--- lib\n%s\n--- main\n%s", seed, seed, want, got, wantRuns, runs, strings.Join(full, "\n"), libSrc, mainSrc)
		}
	}
}

// The kinds of a generated type of main and the forms of one function.
const (
	rkRecord = iota
	rkUnion
	rkDefined
	rkGeneric
)

const (
	rfFits = iota
	rfMissing
	rfWider
)

type relModel struct {
	r     *rand.Rand
	ops   int   // Op0 … Op(ops-1)
	inc   []int // inc[k]: the operation type Opk includes, or -1
	lines []string
	want  []string // "line code"
	runs  []string // "type law"
}

func newRelModel(seed uint64) *relModel {
	return &relModel{r: rand.New(rand.NewPCG(seed, 0xec1))}
}

func (m *relModel) add(format string, args ...any) int {
	m.lines = append(m.lines, fmt.Sprintf(format, args...))
	return len(m.lines)
}

// closure is the operation types that naming Opk declares: Opk and what it
// includes, at any depth.
func (m *relModel) closure(k int) []int {
	var out []int
	for ; k >= 0; k = m.inc[k] {
		out = append(out, k)
	}
	return out
}

// target is an operation type a type may name: Opk, or Gen[int] as k ==
// m.ops.
func (m *relModel) target(k int) string {
	if k == m.ops {
		return "lib.Gen[int]"
	}
	return fmt.Sprintf("lib.Op%d", k)
}

// opNames are the operation names that naming target k asks for.
func (m *relModel) opNames(k int) []string {
	if k == m.ops {
		return []string{"G"}
	}
	var out []string
	for _, j := range m.closure(k) {
		out = append(out, fmt.Sprintf("A%d", j))
	}
	return out
}

func (m *relModel) program() (string, string) {
	m.ops = 1 + m.r.IntN(3)
	m.inc = make([]int, m.ops)
	var lib strings.Builder
	for k := range m.ops {
		m.inc[k] = -1
		include := ""
		if k > 0 && m.r.IntN(3) > 0 {
			m.inc[k] = k - 1 - m.r.IntN(k)
			include = fmt.Sprintf("..Op%d, ", m.inc[k])
		}
		fmt.Fprintf(&lib, "type Op%d { %sA%d() int, law L%d(v int) { v == v } }\n", k, include, k, k)
	}
	lib.WriteString("type Gen[T] { G() T }\n")

	m.add("import lib")
	m.add("type Bad {}")
	names := []string{"G"}
	for k := range m.ops {
		names = append(names, fmt.Sprintf("A%d", k))
	}
	nTypes := 1 + m.r.IntN(4)
	site := 0
	for i := range nTypes {
		kind := m.r.IntN(4)
		name := fmt.Sprintf("T%d", i)
		ref, param, tparams := name, name, ""
		if kind == rkGeneric {
			ref, param, tparams = name+"[int]", name+"[X]", "[X]"
		}
		// The functions: one form per operation name.
		forms := map[string]int{}
		for _, n := range names {
			forms[n] = m.r.IntN(3)
			if m.r.IntN(2) == 0 {
				forms[n] = rfFits // fits more often than not
			}
		}
		// The clause: up to two distinct targets.
		var clause []int
		for range m.r.IntN(3) {
			k := m.r.IntN(m.ops + 1)
			if !slices.Contains(clause, k) {
				clause = append(clause, k)
			}
		}
		var text []string
		for _, k := range clause {
			text = append(text, m.target(k))
		}
		// Now and then the clause names a record, or one name twice: S108
		// at the clause, and the name adds nothing.
		var clauseS108 int
		if m.r.IntN(6) == 0 {
			text = append(text, "Bad")
			clauseS108++
		}
		if len(clause) > 0 && m.r.IntN(6) == 0 {
			text = append(text, m.target(clause[0]))
			clauseS108++
		}
		tail := ""
		if len(text) > 0 {
			tail = " implements " + strings.Join(text, ", ")
		}
		var ln int
		switch kind {
		case rkRecord:
			ln = m.add("type %s { v int }%s", name, tail)
		case rkUnion:
			ln = m.add("type %s = %sA{} | %sB{}%s", name, name, name, tail)
		case rkDefined:
			ln = m.add("type %s int%s", name, tail)
		case rkGeneric:
			ln = m.add("type %s[X] { v X }%s", name, tail)
		}
		for range clauseS108 {
			m.want = append(m.want, fmt.Sprintf("%d S108", ln))
		}
		for _, k := range clause {
			for _, n := range m.opNames(k) {
				if forms[n] != rfFits {
					m.want = append(m.want, fmt.Sprintf("%d S106", ln))
					break
				}
			}
		}
		for _, n := range names {
			switch forms[n] {
			case rfFits:
				m.add("func %s%s(x %s) int { 0 }", n, tparams, param)
			case rfWider:
				m.add("func %s%s(x %s) int fails Bad { return Bad }", n, tparams, param)
			}
		}
		switch kind {
		case rkRecord:
			m.add("func mk%d() %s { %s{v: 1} }", i, ref, name)
		case rkUnion:
			m.add("func mk%d() %s { %sA }", i, ref, name)
		case rkDefined:
			m.add("func mk%d() %s { %s(1) }", i, ref, name)
		case rkGeneric:
			m.add("func mk%d() %s { %s{v: 1} }", i, ref, ref)
		}
		// The model: the clause closed over the includes.
		declared := map[int]bool{}
		for _, k := range clause {
			if k == m.ops {
				declared[k] = true
				continue
			}
			for _, j := range m.closure(k) {
				declared[j] = true
			}
		}
		// A law of Opj runs for each type that declares Opj and has its
		// operations (a generic type is no provider).
		for j := range m.ops {
			fits := true
			for _, n := range m.opNames(j) {
				fits = fits && forms[n] == rfFits
			}
			if declared[j] && fits && kind != rkGeneric {
				m.runs = append(m.runs, fmt.Sprintf("%s L%d", name, j))
			}
		}
		// One use at each of one or two sites.
		for range 1 + m.r.IntN(2) {
			k := m.r.IntN(m.ops + 1)
			target := m.target(k)
			site++
			var at int
			code := "S108"
			switch m.r.IntN(4) {
			case 0:
				m.add("func use%d(o %s) {}", site, target)
				at = m.add("func site%d() { use%d(mk%d()) }", site, site, i)
			case 1:
				at = m.add("func site%d() %s { mk%d() }", site, target, i)
			case 2:
				at = m.add("let W%d = context { %s: mk%d() }", site, target, i)
				code = "S106"
			case 3:
				m.add("func use%d[P %s](p P) {}", site, target)
				at = m.add("func site%d() { use%d(mk%d()) }", site, site, i)
			}
			if !declared[k] {
				m.want = append(m.want, fmt.Sprintf("%d %s", at, code))
			}
		}
	}
	m.add("func main() {}")
	return lib.String(), strings.Join(m.lines, "\n") + "\n"
}
