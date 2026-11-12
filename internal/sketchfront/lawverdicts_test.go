package sketchfront

import (
	"fmt"
	"maps"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/build"
	"ergo.local/prototype/internal/sketch/eval"
	"ergo.local/prototype/internal/sketch/lawcache"
	"ergo.local/prototype/internal/sketch/lower"
)

// verdictProgram is a generated program of pure laws, each reading its
// own declarations: L1 reads f1 and the let k1, L2 reads f2 and k2, L3
// draws a Box through its constructor NewBox, L4 reads f3, which calls
// f1 and reads k1; Wrong is false. Counter.AddsSome runs for the provider
// Tally (its constructor NewTally, its operation Add) and under the named
// context Live, whose provider expression builds a Tally; the machine
// Switch's law Lights reads its step Flip. An edit flips one field.
type verdictProgram struct {
	k1, k2, c, other              int
	f1Swap, f2Swap, boxSwap, f3Ze bool
	l1Flip                        bool
	liveLit, liveParen            bool
	tallyZero, addSwap            bool
	flipSwap                      bool
	comments, moved               bool
}

func (p verdictProgram) source() string {
	live, tally, add, flip := "NewTally()", "Tally{base: 0}", "t.base + n", "Enter[Lamp]{Next: Off}"
	if p.liveLit {
		// Not a Tally literal: one outside NewTally opens Tally's
		// construction, which the run for Tally reads too.
		live = "MakeTally()"
	}
	if p.liveParen {
		live = "(" + live + ")"
	}
	if p.tallyZero {
		tally = "Tally{base: 0 * 1}"
	}
	if p.addSwap {
		add = "n + t.base"
	}
	if p.flipSwap {
		flip = "Enter[Lamp]{Next: On}"
	}
	f1, f2, box, f3, l1 := "x + k1", "x * k2", fmt.Sprintf("n + %d", p.c), "f1(x) - k1", "f1(x) - x == k1"
	if p.f1Swap {
		f1 = "k1 + x"
	}
	if p.f2Swap {
		f2 = "k2 * x"
	}
	if p.boxSwap {
		box = fmt.Sprintf("%d + n", p.c)
	}
	if p.f3Ze {
		f3 = "f1(x) - k1 + 0"
	}
	if p.l1Flip {
		l1 = "k1 == f1(x) - x"
	}
	decls := []string{
		"type Box { v int }",
		"func NewBox(n int) Box { Box{v: " + box + "} }",
		"func f1(x int) int { " + f1 + " }",
		"func f2(x int) int { " + f2 + " }",
		"func f3(x int) int { " + f3 + " }",
		fmt.Sprintf("let k1 = %d", p.k1),
		fmt.Sprintf("let k2 = %d", p.k2),
		"law L1(x int) { " + l1 + " }",
		"law L2(x int) { f2(x) == x * k2 }",
		"law L3(b Box) { b.v - 1 == b.v - 1 }",
		"law L4(x int) { f3(x) == x }",
		"law Wrong(x int) { x + 1 == x }",
		fmt.Sprintf("func other() int { %d }", p.other),
		"type Counter {\n\tAdd(n int) int,\n\tlaw AddsSome(n int) { Add(n) - n == 0 },\n}",
		"type Tally { base int }",
		"func NewTally() Tally { " + tally + " }",
		"func MakeTally() Tally { NewTally() }",
		"func Add(t Tally, n int) int { " + add + " }",
		"let Live = context { Counter: " + live + " }",
		"type Lamp = Off{} | On{}",
		"type Push = Toggle{}",
		"func Flip(l Lamp, _ Push) Decision[Lamp, Lamp] {\n\tmatch l {\n\tcase Off:\n\t\tEnter[Lamp]{Next: On}\n\tcase On:\n\t\t" + flip + "\n\t}\n}",
		"type Switch machine[Lamp, Push] {\n\tinitial: Off,\n\tstep: Flip,\n\tlaw Lights reaches On,\n}",
		"func main() {}",
	}
	if p.moved {
		slices.Reverse(decls)
	}
	if p.comments {
		for i, d := range decls {
			decls[i] = "// a note\n" + strings.Replace(d, "{ ", "{ /* a note */  ", 1)
		}
	}
	return strings.Join(decls, "\n\n") + "\n"
}

// verdictEdit is a generated edit and the model's answer: the laws whose
// verdict it makes stale, read from what the edit touches (the laws'
// read sets above), not from TargetDigest.
type verdictEdit struct {
	name  string
	stale []string
	apply func(*verdictProgram)
}

func verdictEdits(r *rand.Rand) []verdictEdit {
	return []verdictEdit{
		{"f1's body", []string{"L1", "L4"}, func(p *verdictProgram) { p.f1Swap = true }},
		{"the let k1", []string{"L1", "L4"}, func(p *verdictProgram) { p.k1 += 1 + r.IntN(9) }},
		{"f2's body", []string{"L2"}, func(p *verdictProgram) { p.f2Swap = true }},
		{"the let k2", []string{"L2"}, func(p *verdictProgram) { p.k2 += 1 + r.IntN(9) }},
		{"Box's constructor, the generator of L3's cases", []string{"L3"}, func(p *verdictProgram) { p.boxSwap = true }},
		{"f3's body, which L4 alone reads", []string{"L4"}, func(p *verdictProgram) { p.f3Ze = true }},
		{"the law L1", []string{"L1"}, func(p *verdictProgram) { p.l1Flip = true }},
		{"the context Live's provider expression", []string{"Counter.AddsSome under Live"}, func(p *verdictProgram) { p.liveLit = true }},
		{"the tokens of Live's declaration, the same names", []string{"Counter.AddsSome under Live"}, func(p *verdictProgram) { p.liveParen = true }},
		{"NewTally, the constructor Live calls", []string{"Counter.AddsSome for Tally", "Counter.AddsSome under Live"}, func(p *verdictProgram) { p.tallyZero = true }},
		{"Add, Tally's operation", []string{"Counter.AddsSome for Tally", "Counter.AddsSome under Live"}, func(p *verdictProgram) { p.addSwap = true }},
		{"Flip, the machine's step", []string{"Switch.Lights"}, func(p *verdictProgram) { p.flipSwap = true }},
		{"a function no law reads", nil, func(p *verdictProgram) { p.other += 1 + r.IntN(9) }},
		{"comments and spaces", nil, func(p *verdictProgram) { p.comments = true }},
		{"every declaration moved", nil, func(p *verdictProgram) { p.moved = true }},
	}
}

// evaluated is the evaluation of src with the store: the laws that ran,
// by name, with their status.
func evaluated(t *testing.T, src string, store *lawcache.Store) map[string]eval.Status {
	t.Helper()
	a := checked(t, src)
	b := eval.DefaultBudget()
	b.Verdicts = a.EvalVerdicts(store, b.Seed, b.States)
	rep, err := a.Evaluate(b)
	if err != nil {
		t.Fatal(err)
	}
	ran := map[string]eval.Status{}
	for _, res := range rep.Results {
		// Check time skips a run under a context (ergo test's).
		if !res.Cached && res.Status != eval.Skipped {
			ran[strings.TrimPrefix(res.Name, "law ")] = res.Status
		}
	}
	return ran
}

// TestLawVerdictsStale is the store's property (lawcache; v1 audit 3,
// F7): over generated programs and generated edits, an edit of a
// declaration a law reads (its body, a function it calls at any depth, a
// let, the constructor that draws its cases) runs exactly the laws whose
// read set holds it again, and an edit that changes no token of one, or
// touches a declaration no law reads, runs none. A refuted law is never
// stored, so it runs every time. In process: check-time evaluation reads
// and writes the store; ergo test's build keys its runs alike
// (TestVerdicts, targetKey).
//
// The test build's runs are keyed alike (testRuns): a run under a named
// context reads the context's declaration and the constructors its
// provider expressions call, and a machine's search reads its step, each
// as its own key.
//
// Mutants it kills: lc-key-no-digest (the key leaves out the digest, so
// no edit runs a law again), lc-digest-no-generators (the digest leaves
// out the generators, so an edit of NewBox keeps L3 cached),
// lc-context-digest-no-decl (a context's digest leaves out its
// declaration, so an edit of Live keeps its run cached),
// lc-context-runs-uncached and lc-machine-runs-uncached (no run under a
// context, or no search, is a target: it runs every time).
//
// ERGO_VERDICT_SEED=N replays one seed.
func TestLawVerdictsStale(t *testing.T) {
	seeds := []uint64{1, 2, 3, 4}
	if s := os.Getenv("ERGO_VERDICT_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seeds = []uint64{n}
	}
	for _, seed := range seeds {
		r := rand.New(rand.NewPCG(seed, 0x1ac4e))
		p := verdictProgram{k1: 1 + r.IntN(50), k2: 1 + r.IntN(50), c: 1 + r.IntN(50), other: r.IntN(100)}
		for _, e := range verdictEdits(r) {
			fail := func(format string, args ...any) {
				t.Helper()
				t.Fatalf("seed %d, edit %s (replay: ERGO_VERDICT_SEED=%d go test -run TestLawVerdictsStale ./internal/sketchfront): %s\nbase source:\n%s", seed, e.name, seed, fmt.Sprintf(format, args...), p.source())
			}
			store := lawcache.Open(t.TempDir(), "test")
			if ran := evaluated(t, p.source(), store); len(ran) != 7 {
				fail("the first evaluation ran %v, want every law but the one under a context", ran)
			}
			if ran := evaluated(t, p.source(), store); !maps.Equal(ran, map[string]eval.Status{"Wrong": eval.Refuted}) {
				fail("the same source evaluated again ran %v, want only the refuted law", ran)
			}
			// ergo test's build: each run that held prints its ok line.
			native := testRuns(t, p.source(), store, nil)
			if !slices.Contains(native, "Counter.AddsSome under Live") || !slices.Contains(native, "Switch.Lights") {
				fail("the first test build ran %v, want the runs under a context and the machine's search too", native)
			}
			if again := testRuns(t, p.source(), store, nil); !slices.Equal(again, []string{"Wrong"}) {
				fail("the same source built again ran %v, want only the refuted law", again)
			}
			q := p
			e.apply(&q)
			ran := evaluated(t, q.source(), store)
			want := map[string]eval.Status{"Wrong": eval.Refuted}
			wantNative := []string{"Wrong"}
			for _, l := range e.stale {
				if !strings.Contains(l, " under ") {
					want[l] = eval.Passed
				}
				wantNative = append(wantNative, l)
			}
			if !maps.Equal(ran, want) {
				fail("the edited source ran %v, want %v\nedited source:\n%s", ran, want, q.source())
			}
			// The shared runs check time ran again are stored again; the
			// test build runs the rest: its runs under a context, and its
			// own search of the machine.
			wantNative = slices.DeleteFunc(wantNative, func(l string) bool {
				_, shared := want[l]
				return shared && l != "Wrong" && !strings.HasPrefix(l, "Switch.")
			})
			sort.Strings(wantNative)
			if got := testRuns(t, q.source(), store, nil); !slices.Equal(got, wantNative) {
				fail("the edited source's test build ran %v, want %v\nedited source:\n%s", got, wantNative, q.source())
			}
		}
	}
}

// testRuns is a test build of src with the store, in process: each run
// RunTargets names that the --cached file does not list runs, and prints
// its ok line (FAIL for Wrong), and the build's output is recorded. It
// returns the runs that ran, by name without "law ", sorted, and counts
// each in runs when runs is not nil.
func testRuns(t *testing.T, src string, store *lawcache.Store, runs map[string]int) []string {
	t.Helper()
	a := checked(t, src)
	tv := a.TestVerdicts(store, 1, 0)
	cached := map[string]bool{}
	for _, line := range strings.Split(tv.CachedFile(), "\n") {
		if label, _, ok := strings.Cut(line, "\t"); ok {
			cached[label] = true
		}
	}
	var out strings.Builder
	var ran []string
	for _, target := range lower.RunTargets(a.rootPackage()) {
		if cached[target.Label] {
			continue
		}
		ran = append(ran, strings.TrimPrefix(target.Label, "law "))
		if runs != nil {
			runs[target.Label]++
		}
		if target.Label == "law Wrong" {
			out.WriteString("FAIL law Wrong (case 1 of 100): the law is false\n")
			continue
		}
		out.WriteString("ok " + target.Label + " (100 cases)\n")
	}
	tv.Record(out.String())
	sort.Strings(ran)
	return ran
}

// guardSource is the counted guard's program: the ledger's program with
// one site, and pure laws, one of them false.
func guardSource() string {
	p := ledgerProgram{depth: 2, sites: [][2]int{{3, 4}}, base: 11}
	return p.source() + "\nlaw Doubles(x int) { x + x == 2 * x }\n\nlaw Wrong(x int) { x + 1 == x }\n"
}

// TestLawRunsOnce is the counted guard (lawcache; v1 audit 3, F7: "an
// unchanged law runs up to three times"): on an unchanged module, two
// rounds of ergo check, ergo test and the ledger's writer run each law
// run once per verdict, in whichever comes first; a refuted run is never
// stored, so it runs in each place every round. In process: the test
// build's run is replaced by one that runs each target the --cached file
// does not list, and prints its ok or FAIL line (the build's own
// honouring of --cached is TestLawVerdictsCLI's), and the ledger's check
// run by fakeLawChecks, counted.
//
// Mutants it kills: lc-eval-ignores-store (check-time evaluation never
// reads the store), lc-ledger-ignores-store (the ledger's check runs
// every law), lc-store-keeps-failure (the store keeps a refuted check
// run, so the ledger stops running it).
func TestLawRunsOnce(t *testing.T) {
	saved, savedChecks := compilerIdentity, lawChecks
	compilerIdentity = func() (string, error) { return "test", nil }
	defer func() { compilerIdentity, lawChecks = saved, savedChecks }()
	for _, refute := range []bool{false, true} {
		runs := map[string]int{}
		lawChecks = func(BuildRequest, func() (build.Input, error)) func([]lower.File, []string) (string, error) {
			return func(_ []lower.File, laws []string) (string, error) {
				var b strings.Builder
				for _, l := range laws {
					runs["ledger "+l]++
					if refute {
						b.WriteString("FAIL law " + l + " for Tally (case 1 of 400): the sides read differently\n")
						continue
					}
					b.WriteString("ok law " + l + " for Tally (proved: in process)\n")
				}
				return b.String(), nil
			}
		}
		root := t.TempDir()
		store := lawcache.Open(root, "test")
		src := guardSource()
		ledger := filepath.Join(t.TempDir(), lower.LedgerFile)
		for range 2 {
			// ergo check
			for name, status := range evaluated(t, src, store) {
				if status != eval.Skipped {
					runs["law "+name]++
				}
			}
			// ergo test: the build, then the ledger's writer.
			a := checked(t, src)
			tv := a.TestVerdicts(store, 1, 0)
			cached := map[string]bool{}
			for _, line := range strings.Split(tv.CachedFile(), "\n") {
				if label, _, ok := strings.Cut(line, "\t"); ok {
					cached[label] = true
				}
			}
			var out strings.Builder
			for _, target := range lower.RunTargets(a.rootPackage()) {
				if cached[target.Label] {
					continue
				}
				runs[target.Label]++
				if target.Label == "law Wrong" {
					out.WriteString("FAIL law Wrong (case 1 of 100): the law is false\n")
					continue
				}
				out.WriteString("ok " + target.Label + " (100 cases)\n")
			}
			tv.Record(out.String())
			_, err := UpdateLedger(a, BuildRequest{Mode: build.ModeBuild, Source: "/ergo-test/ledger.ergo", Ledger: ledger, Laws: store})
			if err != nil {
				t.Fatal(err)
			}
		}
		want := map[string]int{
			"law Doubles":                 1,
			"law Wrong":                   4, // refuted: evaluated and built, each round
			"law Counter.Joins for Tally": 1, // check-time evaluation skips a rewrite law
			"ledger Counter.Joins":        1,
		}
		if refute {
			want["ledger Counter.Joins"] = 2
		}
		if !maps.Equal(runs, want) {
			keys := slices.Collect(maps.Keys(runs))
			sort.Strings(keys)
			t.Fatalf("refuted check run %v: law runs over two rounds of check, test and ledger: %v (%v), want %v", refute, runs, keys, want)
		}
	}
}
