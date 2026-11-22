package lower

import (
	"flag"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/syntax"
)

// The seeded property "the value rule holds in every body kind"
// (KMEANS-LAW): each seed writes one body that copies a record holding a
// slice and a Map (`let mut y = x`), writes through the copy (an index
// write, a call that borrows it mut, a Put) and checks that the original
// kept its values and the copy took the writes. It hosts the body in each
// kind of body a package lowers: a function a test calls, a test, a
// function literal in a test, a law, and a member law of an operation
// type's provider. Under value semantics every one passes.
//
// TestValueRuleEveryBodyKind runs each seed's package three ways: the
// evaluator at check time, the emitter's test build (the text path), and
// `ergo test`, whose test build lowers every body it can on the IR. All
// three must pass every test and law. Replay: go test -run
// '^TestValueRuleEveryBodyKind$' ./internal/sketch/lower -args -kinds.seed=N
//
// Mutant (lower ledger): law-ir-values-unanalyzed (the law's IR body is
// built before the value rule analyzed it: newBuilder stops the
// lowering, so `ergo test` fails).

var kindsSeed = flag.Uint64("kinds.seed", 0, "run TestValueRuleEveryBodyKind on this seed only")

// kindsSeedCount is the seed count a receipt widens (-kinds.seeds=N).
var kindsSeedCount = flag.Int("kinds.seeds", 3, "run TestValueRuleEveryBodyKind on seeds 1..N")

// kindsNames are the tests and laws each seed's package declares, as
// ergo test labels them.
var kindsNames = []string{"VFunc", "VTest", "VLit", "law VLaw", "law Holder.VMember for Fixed", "law VGeneric"}

// kindsProgram is seed's package.
func kindsProgram(seed uint64) string {
	r := rand.New(rand.NewPCG(seed, 0x6b696e64)) // "kind"
	a := 1 + r.IntN(40)
	// Each write moves the copy's model: the first element's increment,
	// the slice's and the Map's lengths.
	type write struct {
		stmt             string
		add, slice, keys int
	}
	writes := []write{
		{stmt: "y.S[0] = y.S[0] + 7", add: 7},
		{stmt: "y.bump(5)", add: 5},
		{stmt: "y.M = y.M.Put(\"z\", 1)", keys: 1},
		{stmt: "y.S = y.S.Append(9)", slice: 1},
	}
	r.Shuffle(len(writes), func(i, j int) { writes[i], writes[j] = writes[j], writes[i] })
	ws := writes[:1+r.IntN(len(writes))]
	src := "x := mk(a)"
	if r.IntN(2) == 0 {
		src = "x := Bag{S: []int{a, 1}, M: Map[string, int]{\"k\": a}}"
	}
	var body strings.Builder
	fmt.Fprintf(&body, "\tlet %s\n", strings.Replace(src, ":=", "=", 1))
	body.WriteString("\tlet mut y = x\n")
	add, slice, keys := 0, 2, 1
	for _, w := range ws {
		fmt.Fprintf(&body, "\t%s\n", w.stmt)
		add, slice, keys = add+w.add, slice+w.slice, keys+w.keys
	}
	check := fmt.Sprintf("x.S[0] == a && len(x.S) == 2 && len(x.M) == 1 && y.S[0] == a + %d && len(y.S) == %d && len(y.M) == %d", add, slice, keys)
	var b strings.Builder
	b.WriteString("type Bag { S []int, M Map[string, int] }\n\n")
	b.WriteString("func mk(a int) Bag { Bag{S: []int{a, 1}, M: Map[string, int]{\"k\": a}} }\n\n")
	b.WriteString("func bump(b mut Bag, n int) {\n\tb.S[0] += n\n}\n\n")
	fmt.Fprintf(&b, "func fBody(a int) bool {\n%s\t%s\n}\n\n", body.String(), check)
	fmt.Fprintf(&b, "test VFunc {\n\tassert fBody(%d)\n}\n\n", a)
	fmt.Fprintf(&b, "test VTest {\n\tlet a = %d\n%s\tassert %s\n}\n\n", a, body.String(), check)
	fmt.Fprintf(&b, "test VLit {\n\tlet f = func(a int) bool {\n%s\t%s\n\t}\n\tassert f(%d)\n}\n\n", kindsIndent(body.String()), check, a)
	fmt.Fprintf(&b, "law VLaw(a int) {\n%s\t%s\n}\n\n", body.String(), check)
	fmt.Fprintf(&b, "type Holder {\n\tSeed() int,\n\tlaw VMember(a int) {\n%s\t\t%s\n\t},\n}\n\n", kindsIndent(body.String()), check)
	b.WriteString("type Fixed { n int } implements Holder\n\nfunc Seed(f Fixed) int { f.n }\n\n")
	// A generic record holding an int-keyed Map through its parameter,
	// copied in a law and in a generic function the law calls: its Go
	// field is Go's map in every instance (intmap.go intMapFields; std/jobs
	// failed to build when its laws' copies were first analyzed).
	// The record's value is itself a generic record and the functions own
	// their argument, so the emitter writes a clone of each instance: with
	// the field taken for an int map table that clone calls a Clone method
	// Go's map lacks (std/jobs's State[J]; a plain Map[int, T] field took
	// no clone and survived the mutant).
	b.WriteString("type E[T] { v T, n int }\n\ntype Box[T] { M Map[int, E[T]], xs []int }\n\n")
	b.WriteString("func boxOf[T]() Box[T] {\n\tBox[T]{M: Map[int, E[T]]{}, xs: []int{}}\n}\n\n")
	b.WriteString("func put[T](b own Box[T], k int, v T) Box[T] {\n\tlet mut c = b\n\tc.M[k] = E[T]{v: v, n: k}\n\tc.xs = c.xs.Append(k)\n\tc\n}\n\n")
	b.WriteString("func drop[T](b own Box[T], k int) Box[T] {\n\tlet mut c = b\n\tc.M = c.M.Delete(k)\n\tc\n}\n\n")
	b.WriteString("func has[T](b Box[T], k int) bool {\n\tmatch b.M.Get(k) {\n\tcase Some[E[T]] as e:\n\t\te.Value.n == k\n\tcase None:\n\t\tfalse\n\t}\n}\n\n")
	fmt.Fprintf(&b, "law VGeneric(a int) {\n\tlet x = put(boxOf[int](), 1, a)\n\tlet y = put(x, %d, a)\n\tlet z = drop(x, 1)\n\thas(x, 1) && has(y, %[1]d) && !has(z, 1) && len(x.M) == 1 && len(y.M) == 2\n}\n\n", 3+r.IntN(5))
	b.WriteString("func main() needs Console {\n\tConsole.Println(\"x\")\n}\n")
	return b.String()
}

// kindsIndent is body one tab deeper.
func kindsIndent(body string) string {
	return strings.ReplaceAll(strings.TrimSuffix("\t"+body, "\t"), "\n\t", "\n\t\t")
}

// kindsVerdicts are the verdicts out gives each name: "passed",
// "refuted", or "no verdict".
func kindsVerdicts(out string) map[string]string {
	got := map[string]string{}
	for _, name := range kindsNames {
		got[name] = "no verdict"
		for _, l := range lines(out) {
			switch {
			case (l == "ok "+name || strings.HasPrefix(l, "ok "+name+" ")) && strings.Contains(l, "(evaluated)"):
				// The evaluator decided it, so the IR never ran it: ergo
				// test evaluates a package whose native build it does not
				// make (KMEANS-LAW: with intMapFields off, VGeneric's
				// build fails and the run passed evaluated).
				got[name] = "evaluated, not run natively"
			case l == "ok "+name || strings.HasPrefix(l, "ok "+name+" "):
				got[name] = "passed"
			case strings.HasPrefix(l, "FAIL "+name+" ") || strings.HasPrefix(l, "FAIL "+name+":"):
				got[name] = "refuted"
			}
		}
	}
	return got
}

// TestNewBuilderRefusesUnanalyzed: an IR body started from a declaration
// the value rule has not analyzed stops the lowering (newBuilder), and
// one it has analyzed starts. The ordering is the guard itself; no caller
// gets it wrong today, so only this test kills ir-builder-unasserted.
func TestNewBuilderRefusesUnanalyzed(t *testing.T) {
	fc := &funcCtx{l: &lowerer{}}
	st := &irState{}
	root := &syntax.FuncDecl{}
	stopped := func() (r any) {
		defer func() { r = recover() }()
		st.newBuilder(fc, root)
		return nil
	}
	if r := stopped(); r == nil {
		t.Fatal("newBuilder started the IR body of an unanalyzed declaration")
	} else if _, ok := r.(refusal); !ok {
		t.Fatalf("newBuilder panicked with %v, not a located refusal", r)
	}
	fc.l.valueFacts().roots[root] = &valueWalk{}
	if r := stopped(); r != nil {
		t.Fatalf("newBuilder refused an analyzed declaration: %v", r)
	}
	if r := func() (r any) {
		defer func() { r = recover() }()
		st.newBuilder(fc, nil)
		return nil
	}(); r != nil {
		t.Fatalf("newBuilder refused a body with no source statements: %v", r)
	}
}

// TestValueRuleEveryBodyKind is the property above.
//
// It builds Go because the question is what the test builds decide at run
// time, on the text path and on the IR, which only a run shows; the IR's
// test build needs ergo's IR compiler.
func TestValueRuleEveryBodyKind(t *testing.T) {
	needsBuild(t)
	seeds := []uint64{*kindsSeed}
	if *kindsSeed == 0 {
		seeds = nil
		for s := uint64(1); s <= uint64(max(*kindsSeedCount, 1)); s++ {
			seeds = append(seeds, s)
		}
	}
	var builds []*lowered
	srcs := map[uint64]string{}
	for _, seed := range seeds {
		srcs[seed] = kindsProgram(seed)
		builds = append(builds, lowerTestBuild(t, fmt.Sprintf("kinds%d", seed), srcs[seed]))
	}
	exes := buildAll(t, builds, true)
	if exes == nil {
		t.Fatal("the body-kind programs do not build")
	}
	for _, seed := range seeds {
		src := srcs[seed]
		replay := fmt.Sprintf("Replay: go test -run '^TestValueRuleEveryBodyKind$' ./internal/sketch/lower -args -kinds.seed=%d\n%s", seed, src)
		rep, err := evaluate(src)
		if err != nil {
			t.Fatalf("seed %d: the evaluator: %v\n%s", seed, err, replay)
		}
		for _, res := range rep.Results {
			if res.Status != "passed" {
				t.Errorf("seed %d: the evaluator says %s %s: %s\n%s", seed, res.Name, res.Status, res.Reason, replay)
			}
		}
		text, _, _ := runExeArgs(t, exes[fmt.Sprintf("kinds%d", seed)])
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "kinds.ergo"), []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(ergoCLI(t), "test", dir, "--native", "--count", "1")
		cmd.Dir = dir
		irOut, irErr := cmd.CombinedOutput()
		if irErr != nil {
			// A failed run names its exit (a build error, a kill) beside
			// the output, so a verdict it lacks shows why.
			t.Errorf("seed %d: ergo test --native: %v\n--- output\n%s\n%s", seed, irErr, irOut, replay)
		}
		for _, run := range []struct{ name, out string }{{"the text path", text}, {"ergo test (the IR)", string(withoutBuildNote(irOut))}} {
			for name, v := range kindsVerdicts(run.out) {
				if v != "passed" {
					t.Errorf("seed %d: %s says %s %s\n--- output\n%s\n%s", seed, run.name, name, v, run.out, replay)
				}
			}
		}
	}
}
