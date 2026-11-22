package eval

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

// TestGenerationEndsOrIsRefused is the seeded property of generation over
// type graphs with cycles (BUILDER-WELLFOUNDED): records, unions of
// records, and operation types each with one builder (its provider's
// New<P>), whose fields and parameters name each other, slices included.
// A reference model, the least fixpoint of "has a finite value" over the
// graph, decides each type: a property over a type every type it reaches
// has a finite value is checked and passes at check time; any other is
// refused by the checker (S108, "has no finite value"). A provider's law
// runs when its values are generated, else it is skipped with the same
// reason. Generation never stops at the depth bound: before the fix, an
// operation type whose one builder takes its own type, directly or
// through a cycle (llm's NewMetered(inner Model)), was admitted and every
// generation of it recursed until the bound (and, in the Go build, until
// the stack overflowed). A failing seed prints the source, the model's
// verdicts and the replay command.
//
// Mutants it alone catches (testdata/mutants.json): bw-builder-ranks-zero
// (an operation type ranks 0 whatever its builder takes) and
// bw-builder-params-unreached (the ranks do not reach the builder's
// parameters).
func TestGenerationEndsOrIsRefused(t *testing.T) {
	first, last := uint64(1), uint64(96)
	if s := os.Getenv("ERGO_BW_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		first, last = n, n
	}
	reach := map[string]int{}
	for seed := first; seed <= last; seed++ {
		g := newTypeGraph(seed)
		admitted := map[string]bool{}
		for _, n := range g.names {
			admitted[n] = g.admissible(n)
		}
		replay := fmt.Sprintf("replay: ERGO_BW_SEED=%d go test -run '^TestGenerationEndsOrIsRefused$' ./internal/sketch/eval", seed)
		// Every type has a property; the checker refuses exactly the
		// properties over a type the model says has no finite value.
		src := g.source(g.names)
		f, err := syntax.Parse(src)
		if err != nil {
			t.Fatalf("seed %d: %v\n%s", seed, err, src)
		}
		_, diags := check.Check("main", []*syntax.File{f}, nil)
		refused := map[string]bool{}
		for _, d := range diags {
			line := strings.Split(src, "\n")[d.Line-1]
			name, ok := strings.CutPrefix(line, "test Gen")
			if !ok || d.Code != check.CodeType || !strings.Contains(d.Message, "has no finite value") {
				t.Fatalf("seed %d: unexpected diagnostic %v\n%s\n%s", seed, d, src, replay)
			}
			refused[name[:strings.IndexByte(name, '(')]] = true
		}
		var kept []string
		for _, n := range g.names {
			if refused[n] == admitted[n] {
				t.Fatalf("seed %d: %s: model says finite=%v, checker refused=%v\n%s\n%s", seed, n, admitted[n], refused[n], src, replay)
			}
			if admitted[n] {
				kept = append(kept, n)
				reach["admitted "+g.kind[n]]++
			} else {
				reach["refused "+g.kind[n]]++
			}
		}
		// The admitted properties and every provider's law run to an
		// end: passed, or a provider with no generator skipped.
		src = g.source(kept)
		pkg := checked(t, src)
		b := DefaultBudget()
		b.Seed = seed
		rep, err := Run(pkg, b)
		if err != nil {
			t.Fatalf("seed %d: %v\n%s", seed, err, src)
		}
		for _, r := range rep.Results {
			want := Passed
			if p, ok := strings.CutPrefix(r.Name, "law "); ok {
				prov := p[strings.LastIndex(p, " for ")+len(" for "):]
				if !g.admissible(prov) {
					want = Skipped
					reach["law skipped"]++
					if !strings.Contains(r.Reason, "has no finite value") {
						t.Fatalf("seed %d: %s is skipped for %q, want no finite value\n%s\n%s", seed, r.Name, r.Reason, src, replay)
					}
				} else {
					reach["law passed"]++
				}
			}
			if r.Status != want {
				t.Fatalf("seed %d: %s is %s (%s), want %s\n%s\n%s", seed, r.Name, r.Status, r.Reason, want, src, replay)
			}
		}
	}
	if first == 1 {
		for _, k := range []string{"admitted record", "refused record", "admitted union", "refused union", "admitted ops", "refused ops", "law passed", "law skipped"} {
			if reach[k] == 0 {
				t.Errorf("no seed reached %s: %v", k, reach)
			}
		}
	}
}

// typeGraph is one seed's types: records R*, unions U* of records, and
// operation types I* each provided by P* whose New<P> is its one builder.
// edges are the types each draws: a record's or provider's fields, a
// union's variants. A field is int, a type, or a slice of a type.
type typeGraph struct {
	names  []string            // the types a property is written over
	kind   map[string]string   // record, union, ops, provider
	fields map[string][]string // record or provider fields, as written
	vars   map[string][]string // union variants
}

func newTypeGraph(seed uint64) *typeGraph {
	r := rand.New(rand.NewPCG(seed, 0xb0f))
	g := &typeGraph{kind: map[string]string{}, fields: map[string][]string{}, vars: map[string][]string{}}
	nr, nu, ni := 1+r.IntN(4), r.IntN(3), r.IntN(4)
	var recs, all []string
	for i := range nr {
		recs = append(recs, fmt.Sprintf("R%d", i))
	}
	all = append(all, recs...)
	for i := range nu {
		all = append(all, fmt.Sprintf("U%d", i))
	}
	for i := range ni {
		all = append(all, fmt.Sprintf("I%d", i))
	}
	for _, n := range all {
		if n[0] == 'U' {
			g.kind[n] = "union"
			a := r.IntN(len(recs))
			g.vars[n] = []string{recs[a]}
			if len(recs) > 1 {
				g.vars[n] = append(g.vars[n], recs[(a+1+r.IntN(len(recs)-1))%len(recs)])
			}
		}
	}
	// field is a field of a record (below, the record's index) or of a
	// provider (below = nr). A record holds by value only records declared
	// before it, itself or through a union's variants, since a record that
	// contains itself by value is refused before generation is asked
	// (types.md, Structs 4); else it holds a slice. So a cycle with no
	// finite value goes through an operation type's builder.
	before := func(t string, below int) bool {
		switch t[0] {
		case 'R':
			return int(t[1]-'0') < below
		case 'U':
			return !slices.ContainsFunc(g.vars[t], func(v string) bool { return int(v[1]-'0') >= below })
		}
		return true
	}
	field := func(below int) string {
		t := all[r.IntN(len(all))]
		switch k := r.IntN(6); {
		case k == 0:
			return "int"
		case k == 1 || !before(t, below):
			return "[]" + t
		}
		return t
	}
	for i, n := range all {
		switch n[0] {
		case 'R':
			g.kind[n] = "record"
			for range 1 + r.IntN(3) {
				g.fields[n] = append(g.fields[n], field(i))
			}
		case 'I':
			g.kind[n] = "ops"
			p := "P" + n[1:]
			g.kind[p] = "provider"
			for range r.IntN(3) {
				g.fields[p] = append(g.fields[p], field(nr))
			}
		}
	}
	g.names = all
	return g
}

// edges are the types a value of n is built from.
func (g *typeGraph) edges(n string) []string {
	var out []string
	switch g.kind[n] {
	case "union":
		return g.vars[n]
	case "ops":
		n = "P" + n[1:]
	}
	for _, f := range g.fields[n] {
		if f = strings.TrimPrefix(f, "[]"); f != "int" {
			out = append(out, f)
		}
	}
	return out
}

// admissible is the model's verdict: every type n reaches has a finite
// value, the least fixpoint where a slice is finite (empty), a record or
// a provider when each field is, a union when a variant is, an operation
// type when its provider is (its builder takes the provider's fields).
func (g *typeGraph) admissible(n string) bool {
	finite := map[string]bool{}
	ok := func(f string) bool { return f == "int" || strings.HasPrefix(f, "[]") || finite[f] }
	for changed := true; changed; {
		changed = false
		for m := range g.kind {
			if finite[m] {
				continue
			}
			var v bool
			switch g.kind[m] {
			case "union":
				v = slices.ContainsFunc(g.vars[m], ok)
			case "ops":
				v = finite["P"+m[1:]]
			default:
				v = !slices.ContainsFunc(g.fields[m], func(f string) bool { return !ok(f) })
			}
			if v {
				finite[m], changed = true, true
			}
		}
	}
	seen := map[string]bool{}
	var walk func(m string) bool
	walk = func(m string) bool {
		if seen[m] {
			return true
		}
		seen[m] = true
		if !finite[m] {
			return false
		}
		for _, e := range g.edges(m) {
			if !walk(e) {
				return false
			}
		}
		return true
	}
	return walk(n)
}

// source is the program: every type, and a property over each of props.
func (g *typeGraph) source(props []string) string {
	var b strings.Builder
	record := func(n string, extra string) {
		var fs []string
		for i, f := range g.fields[n] {
			fs = append(fs, fmt.Sprintf("F%d %s", i, f))
		}
		fmt.Fprintf(&b, "type %s { %s }%s\n", n, strings.Join(fs, ", "), extra)
	}
	for _, n := range g.names {
		switch g.kind[n] {
		case "record":
			record(n, "")
		case "union":
			fmt.Fprintf(&b, "type %s = %s\n", n, strings.Join(g.vars[n], " | "))
		case "ops":
			p, i := "P"+n[1:], n[1:]
			fmt.Fprintf(&b, "type %s { Op%s() int, law Zero%s() { Op%s() == 0 } }\n", n, i, i, i)
			record(p, " implements "+n)
			fmt.Fprintf(&b, "func Op%s(p %s) int { 0 }\n", i, p)
			var ps, fs []string
			for k, f := range g.fields[p] {
				ps = append(ps, fmt.Sprintf("f%d %s", k, f))
				fs = append(fs, fmt.Sprintf("F%d: f%d", k, k))
			}
			lit := p // a record with no fields is written bare (types.md, Structs 2)
			if len(fs) > 0 {
				lit = p + "{" + strings.Join(fs, ", ") + "}"
			}
			fmt.Fprintf(&b, "func New%s(%s) %s { %s }\n", p, strings.Join(ps, ", "), p, lit)
		}
	}
	for _, n := range props {
		fmt.Fprintf(&b, "test Gen%s(x %s) { assert true }\n", n, n)
	}
	return b.String()
}
