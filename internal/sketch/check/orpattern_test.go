package check_test

import (
	"fmt"
	"math/rand/v2"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/eval"
	"ergo.local/prototype/internal/sketch/format"
)

// TestNestedAlternativesMatchTheirExpansion (FK7a) is a seeded property of
// nested or-patterns against two references. Each seed writes one match
// over two union values, as a tuple subject or as a record's fields, whose
// cases nest alternatives with | (a tuple element, a record field, and a
// parenthesised group bound with as), and the same match with every
// alternative written out as a pattern of its own, with commas. Then: the
// or-program formats to itself; both programs check with the same set
// of diagnostics (code and line); and when they check clean, a reference
// model (first case that matches, by Go) gives each of the six subject
// values its case, which the evaluator confirms on the or-program.
// ERGO_SEED=<n> replays one seed; a failure prints both programs.
//
// Mutants it kills that no other test does (syntax ledger): the expansion
// keeps only an or-pattern's first alternative; the product of a tuple's
// alternatives drops a combination; a record field's alternatives are not
// expanded; a parenthesised group's alternatives are not expanded; the
// formatter prints one alternative.
func TestNestedAlternativesMatchTheirExpansion(t *testing.T) {
	var seeds []uint64
	if s := os.Getenv("ERGO_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seeds = []uint64{n}
	} else {
		for i := range uint64(300) {
			seeds = append(seeds, i+1)
		}
	}
	kinds := map[string]int{}
	for _, seed := range seeds {
		g := orGen(rand.New(rand.NewPCG(seed, 0x0a1)))
		for k := range g.reached {
			kinds[k]++
		}
		fail := func(format string, args ...any) {
			t.Helper()
			t.Fatalf("seed %d: %s\nReplay: ERGO_SEED=%d go test -run '^TestNestedAlternativesMatchTheirExpansion$' ./internal/sketch/check\nor:\n%s\nexpanded:\n%s", seed, fmt.Sprintf(format, args...), seed, g.or, g.expanded)
		}
		if out, err := format.Source(g.or); err != nil || out != g.or {
			fail("format gives (%v):\n%s", err, out)
		}
		pkg, diags := check.CheckSource(g.or)
		_, want := check.CheckSource(g.expanded)
		if a, b := diagLines(diags), diagLines(want); a != b {
			fail("diagnostics differ:\nor       %s\nexpanded %s", a, b)
		}
		if len(diags) > 0 {
			kinds["refused"]++
			continue
		}
		kinds["clean"]++
		rep, err := eval.Run(pkg, eval.DefaultBudget())
		if err != nil {
			fail("eval: %v", err)
		}
		if len(rep.Results) != 1 || rep.Results[0].Status != eval.Passed {
			fail("the reference's cases do not hold: %+v", rep.Results)
		}
	}
	t.Logf("%d seeds reach: %v", len(seeds), kinds)
	if len(seeds) > 1 {
		for _, k := range []string{"tuple", "record", "group-as", "default", "comma", "clean", "refused"} {
			if kinds[k] == 0 {
				t.Errorf("no seed reaches %s", k)
			}
		}
	}
}

// diagLines is the set of a program's diagnostics as code and line. A set:
// the alternatives of one written pattern share its span, so the checker
// reports one diagnostic where the written-out patterns get one each.
func diagLines(ds []check.Diagnostic) string {
	seen := map[string]bool{}
	var out []string
	for _, d := range ds {
		k := fmt.Sprintf("%s:%d", d.Code, d.Line)
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return strings.Join(out, " ")
}

// orProgram is one generated match, written with nested alternatives and
// written out, and the kinds of site it reached.
type orProgram struct {
	or, expanded string
	reached      map[string]bool
}

// orCell is one subject's pattern in a case: the variants it matches (nil
// is _), and whether it is a parenthesised group bound with as.
type orCell struct {
	vs []int
	as bool
}

func orGen(r *rand.Rand) orProgram {
	reached := map[string]bool{}
	sizes := [2]int{3, 2}
	names := [2]string{"A", "B"}
	record := r.IntN(2) == 0
	if record {
		reached["record"] = true
	} else {
		reached["tuple"] = true
	}
	// cell draws one subject's pattern.
	cell := func(k int) orCell {
		if r.IntN(4) == 0 {
			return orCell{}
		}
		var vs []int
		for v := range sizes[k] {
			if r.IntN(2) == 0 {
				vs = append(vs, v)
			}
		}
		if len(vs) == 0 {
			vs = []int{r.IntN(sizes[k])}
		}
		r.Shuffle(len(vs), func(i, j int) { vs[i], vs[j] = vs[j], vs[i] })
		return orCell{vs: vs}
	}
	// alt spells a cell's pattern with |; one spells one alternative.
	alt := func(k int, c orCell) string {
		if c.vs == nil {
			return "_"
		}
		var parts []string
		for _, v := range c.vs {
			parts = append(parts, fmt.Sprintf("%s%d", names[k], v))
		}
		s := strings.Join(parts, " | ")
		if c.as {
			s = "(" + s + ") as w"
		}
		return s
	}
	one := func(k int, c orCell, v int) string {
		if c.vs == nil {
			return "_"
		}
		s := fmt.Sprintf("%s%d", names[k], v)
		if c.as {
			s = "(" + s + ") as w"
		}
		return s
	}
	shape := func(a, b string) string {
		if record {
			return "Pair{x: " + a + ", y: " + b + "}"
		}
		return "(" + a + ", " + b + ")"
	}
	type arm struct {
		pats [][2]orCell
	}
	var arms []arm
	for range 1 + r.IntN(4) {
		var a arm
		for range 1 + r.IntN(2) {
			a.pats = append(a.pats, [2]orCell{cell(0), cell(1)})
		}
		if len(a.pats) > 1 {
			reached["comma"] = true
		}
		// A group bound with as: every pattern of the case binds w.
		if r.IntN(4) == 0 {
			reached["group-as"] = true
			for i := range a.pats {
				if a.pats[i][0].vs == nil {
					a.pats[i][0].vs = []int{r.IntN(sizes[0])}
				}
				a.pats[i][0].as = true
			}
		}
		arms = append(arms, a)
	}
	def := r.IntN(2) == 0
	if def {
		reached["default"] = true
	}
	var o, e strings.Builder
	both := func(s string) { o.WriteString(s); e.WriteString(s) }
	both("type A = A0{} | A1{} | A2{}\n\ntype B = B0{} | B1{}\n\n")
	if record {
		both("type Pair { x A, y B }\n\n")
	}
	both("func use(w A) int {\n\t0\n}\n\n")
	both("func f(a A, b B) int {\n")
	if record {
		both("\tlet p = Pair{x: a, y: b}\n\tmatch p {\n")
	} else {
		both("\tmatch (a, b) {\n")
	}
	for i, a := range arms {
		var op, ep []string
		for _, p := range a.pats {
			op = append(op, shape(alt(0, p[0]), alt(1, p[1])))
			xs, ys := p[0].vs, p[1].vs
			if xs == nil {
				xs = []int{-1}
			}
			if ys == nil {
				ys = []int{-1}
			}
			for _, x := range xs {
				for _, y := range ys {
					ep = append(ep, shape(one(0, p[0], x), one(1, p[1], y)))
				}
			}
		}
		body := strconv.Itoa(i + 1)
		if a.pats[0][0].as {
			body += " + use(w)"
		}
		o.WriteString("\tcase " + strings.Join(op, ", ") + ":\n\t\t" + body + "\n")
		e.WriteString("\tcase " + strings.Join(ep, ", ") + ":\n\t\t" + body + "\n")
	}
	if def {
		both("\tcase _:\n\t\t0\n")
	}
	both("\t}\n}\n\n")
	// The reference: the first case whose pattern matches.
	matches := func(c orCell, v int) bool {
		if c.vs == nil {
			return true
		}
		for _, x := range c.vs {
			if x == v {
				return true
			}
		}
		return false
	}
	both("test Cases {\n")
	for a := range sizes[0] {
		for b := range sizes[1] {
			want := -1
			for i, arm := range arms {
				for _, p := range arm.pats {
					if want < 0 && matches(p[0], a) && matches(p[1], b) {
						want = i + 1
					}
				}
			}
			if want < 0 {
				want = 0 // the default, or no case: then the match is refused
			}
			both(fmt.Sprintf("\tassert f(A%d, B%d) == %d\n", a, b, want))
		}
	}
	both("}\n\nfunc main() {}\n")
	return orProgram{or: o.String(), expanded: e.String(), reached: reached}
}
