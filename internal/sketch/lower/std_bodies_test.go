package lower

import (
	"fmt"
	"math/rand/v2"
	"os"
	"strconv"
	"strings"
	"testing"
)

// The comparing law of an NP4f move (np4f-plan §6.1): a std function that
// moves from an extern of the Ergo runtime to an import go call or an Ergo
// body keeps its meaning, so for generated inputs the new std function
// prints what the old Go body (runtime/v1, called from Go) returns. The
// move unit adds the package's cases; the next unit deletes them with the
// old body.

// bodyCase is one moved function: an Ergo function of one parameter x
// that prints the new std function's answer, and per input the Ergo literal
// and the old body's answer, printed the same way.
type bodyCase struct {
	name    string // the std function, as package.Name
	imports []string
	param   string // the Ergo type of x
	call    string // an Ergo string expression over x
	lits    []string
	wants   []string
}

// bodyRows erases a typed case: edges first, then n values of gen from the
// seeded r; lit is v's Ergo literal of type param, ref the old body's answer.
func bodyRows[T any](name string, imports []string, param, call string, edges []T, n int, r *rand.Rand, gen func(*rand.Rand) T, lit func(T) string, ref func(T) string) bodyCase {
	c := bodyCase{name: name, imports: imports, param: param, call: call}
	add := func(v T) {
		c.lits = append(c.lits, lit(v))
		c.wants = append(c.wants, ref(v))
	}
	for _, v := range edges {
		add(v)
	}
	for range n {
		add(gen(r))
	}
	return c
}

// bodySeed is the seed of a comparing law: ERGO_BODIES_SEED, or 1.
func bodySeed(t *testing.T) uint64 {
	t.Helper()
	if s := os.Getenv("ERGO_BODIES_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatalf("ERGO_BODIES_SEED=%q: %v", s, err)
		}
		return n
	}
	return 1
}

// bodyProgram is the Ergo program of the cases: a function per case, and a
// main that prints one line per input.
func bodyProgram(cases []bodyCase) string {
	var b strings.Builder
	seen := map[string]bool{}
	for _, c := range cases {
		for _, imp := range c.imports {
			if !seen[imp] {
				seen[imp] = true
				b.WriteString("import " + imp + "\n")
			}
		}
	}
	for i, c := range cases {
		fmt.Fprintf(&b, "\n// %s\nfunc case%d(x %s) string { %s }\n", c.name, i, c.param, c.call)
	}
	b.WriteString("\nfunc main() needs Console {\n")
	for i, c := range cases {
		for _, l := range c.lits {
			fmt.Fprintf(&b, "\tConsole.Println(case%d(%s))\n", i, l)
		}
	}
	b.WriteString("}\n")
	return b.String()
}

// bodyDisagreements are the inputs where the program's lines, out, differ
// from the old bodies' answers, one problem per case at most.
func bodyDisagreements(cases []bodyCase, out string) []string {
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	var problems []string
	k := 0
	for _, c := range cases {
		for j, l := range c.lits {
			got := "<no line>"
			if k < len(lines) {
				got = lines[k]
			}
			k++
			if got != c.wants[j] {
				problems = append(problems, fmt.Sprintf("%s(%s): the new function prints %q, the old body %q", c.name, l, got, c.wants[j]))
				break
			}
		}
	}
	return problems
}

// compareBodies builds and runs the cases' program and fails at each
// disagreement with the Ergo source, both answers and the replay command.
//
// It builds Go because the old body is Go that runs only in a built
// program's runtime and the new one is the lowered Ergo; the question is
// what each returns, which no in-process check observes.
func compareBodies(t *testing.T, test string, seed uint64, cases []bodyCase) {
	t.Helper()
	src := bodyProgram(cases)
	o := mustLower(t, "bodies", src)
	exes := buildAll(t, []*lowered{o}, true)
	if exes == nil {
		t.Fatal("the comparing program does not build")
	}
	out, stderr, code := runExe(t, exes["bodies"])
	if code != 0 {
		t.Fatalf("the comparing program exits %d: %s\n%s", code, firstLines(stderr, 12), src)
	}
	if p := bodyDisagreements(cases, out); len(p) > 0 {
		t.Errorf("seed %d: the moved functions differ from their old bodies:\n%s\nprogram:\n%s\nreplay: ERGO_BODIES_SEED=%d go test -run '^%s$' ./internal/sketch/lower", seed, strings.Join(p, "\n"), src, seed, test)
	}
}

// TestBodyLawFindsADifference is the comparing law's own check: a case
// whose reference differs from the Ergo function on one input is reported,
// and the agreeing case is not, so a law that compared the new function
// with itself (mutant np4f-law-compares-self) fails here.
//
// It builds Go because the law it checks runs a built program
// (compareBodies).
func TestBodyLawFindsADifference(t *testing.T) {
	needsBuild(t)
	r := rand.New(rand.NewPCG(bodySeed(t), 0xb0d1e5))
	gen := func(r *rand.Rand) int64 { return r.Int64N(1<<20) - 1<<19 }
	lit := func(v int64) string { return strconv.FormatInt(v, 10) }
	same := bodyRows("fixture.double", nil, "int", `"\(x * 2)"`, []int64{0, -1}, 8, r, gen, lit, func(v int64) string { return strconv.FormatInt(2*v, 10) })
	planted := bodyRows("fixture.planted", nil, "int", `"\(x * 2)"`, []int64{0, 7}, 8, r, gen, lit, func(v int64) string {
		if v == 7 {
			return "15"
		}
		return strconv.FormatInt(2*v, 10)
	})
	cases := []bodyCase{same, planted}
	src := bodyProgram(cases)
	o := mustLower(t, "bodies", src)
	exes := buildAll(t, []*lowered{o}, true)
	if exes == nil {
		t.Fatal("the fixture program does not build")
	}
	out, stderr, code := runExe(t, exes["bodies"])
	if code != 0 {
		t.Fatalf("the fixture program exits %d: %s\n%s", code, firstLines(stderr, 12), src)
	}
	p := bodyDisagreements(cases, out)
	if len(p) != 1 || !strings.HasPrefix(p[0], "fixture.planted(7)") {
		t.Errorf("want one disagreement, fixture.planted(7); got %q\nprogram:\n%s", p, src)
	}
}
