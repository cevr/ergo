package lower

import (
	"flag"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
)

// The flat run's seeded property (MU8; machinerun.go; common.md, lowering
// rule). Each seed writes machines whose runs the flat lowering takes:
// a state union of one to three record variants (an int field, and in
// some variants a string or a slice); signals of one
// to three variants; and a step that reaches every way the flat step
// returns and matches:
//
//   - a decision literal whose Next is a variant literal, the state s, a
//     bound variant, or a call that returns the union (flatReturn, flatOf);
//   - a decision that is a call of a function returning the union, a let
//     of a variant's type or of the union, a bare Ignore or Reject, an
//     explicit return, and an if (decideOf);
//   - a match on the state alone (a Go type switch) or on the pair (s, e)
//     (a match tree), with a final wildcard in some programs (flatTags);
//   - a step with a capability row (it takes the environment) and a step
//     with an error row (it keeps std's loop).
//
// main prints, for each generated signal list, the run over every prefix
// beside machines.RunTimed over the same prefix at time 0 (std's loop, the
// reference: the line holds both), and machines.From from a generated
// state over the whole list. Each program is lowered flat and with the
// reference lowering (machineLowering "interface"): every pair agrees, and
// the two programs print the same lines.
//
// A failure prints the seed, the Ergo source and both outputs.
// Replay: go test -run '^TestMachineFlatRunAgrees$' ./internal/sketch/lower -args -flatrun.seed=N

var flatRunSeed = flag.Uint64("flatrun.seed", 0, "run TestMachineFlatRunAgrees on this seed alone")

// flatGen writes one program.
type flatGen struct {
	r       *rand.Rand
	reached map[string]bool
	states  []string // the state variants' names
	fields  []string // per variant: "", "t" (a string) or "xs" (a slice)
	signals int
}

// stateLit is a literal of state variant v with n.
func (g *flatGen) stateLit(v int, n string) string {
	switch g.fields[v] {
	case "t":
		g.reached["string field"] = true
		return fmt.Sprintf("%s{n: %s, t: \"t\\(%s)\"}", g.states[v], n, n)
	case "xs":
		g.reached["slice field"] = true
		return fmt.Sprintf("%s{n: %s, xs: []int{%s, 1}}", g.states[v], n, n)
	}
	return fmt.Sprintf("%s{n: %s}", g.states[v], n)
}

// next is a Next of type St that the arm of variant v writes.
func (g *flatGen) next(v int) string {
	sum := fmt.Sprintf("a.n + x.v + %d", g.r.IntN(5)-2)
	switch g.r.IntN(5) {
	case 0:
		g.reached["next s"] = true
		return "s"
	case 4:
		// The state read as a value (passed to num): the flat step's
		// entry puts it back into its union (flatEntry).
		g.reached["state read"] = true
		return "pick(" + sum + " + num(s))"
	case 1:
		g.reached["next bound"] = true
		return "a"
	case 2:
		g.reached["next call"] = true
		return "pick(" + sum + ")"
	}
	g.reached["next variant"] = true
	return g.stateLit(g.r.IntN(len(g.states)), sum)
}

// literal is a decision literal of the arm of variant v.
func (g *flatGen) literal(v int) string {
	switch g.r.IntN(4) {
	case 0:
		return "Enter[St]{Next: " + g.next(v) + "}"
	case 1:
		g.reached["emit"] = true
		return "Emit[St, int]{Next: " + g.next(v) + ", Output: a.n + x.v}"
	case 2:
		g.reached["done"] = true
		return "Done[int]{Output: a.n * 2 + x.v}"
	}
	return "Stay[St]{Next: " + g.next(v) + "}"
}

// arm is the body of the arm of state variant v and a signal x, indented
// by ind; a has v's type. It reads both
// bindings first (S110: every arm reads its bindings).
func (g *flatGen) arm(v int, ind string, fallible bool) string {
	return ind + "let _ = a.n + x.v\n" + g.armBody(v, ind, fallible)
}

func (g *flatGen) armBody(v int, ind string, fallible bool) string {
	switch k := g.r.IntN(12); {
	case k < 4:
		g.reached["literal"] = true
		return ind + g.literal(v) + "\n"
	case k == 4:
		g.reached["union call"] = true
		return ind + "deciding(a.n + x.v)\n"
	case k == 5:
		g.reached["bare"] = true
		bare := []string{"Ignore", "Reject"}[g.r.IntN(2)]
		if bare == "Reject" && !fallible {
			// A Reject in a run the flat loop takes (the mutant "loop").
			g.reached["flat reject"] = true
		}
		return ind + bare + "\n"
	case k == 6:
		g.reached["let variant"] = true
		return ind + "let d = Stay[St]{Next: " + g.next(v) + "}\n" + ind + "d\n"
	case k == 7:
		g.reached["let union"] = true
		return ind + "let d Decision[St, int] = " + g.literal(v) + "\n" + ind + "d\n"
	case k == 8:
		g.reached["return"] = true
		return ind + "return " + g.literal(v) + "\n"
	case k == 9:
		g.reached["if"] = true
		return ind + "if x.v > 0 {\n" + ind + "\t" + g.literal(v) + "\n" + ind + "} else {\n" + ind + "\tIgnore\n" + ind + "}\n"
	case k >= 10 && fallible:
		g.reached["fail"] = true
		return ind + "return Boom\n"
	}
	return ind + g.literal(v) + "\n"
}

// flatRunProgram is the program of seed and k, its name and whether its
// runs take the flat lowering.
func flatRunProgram(seed uint64, k int, reached map[string]bool) (name, src string, flat bool) {
	g := &flatGen{r: rand.New(rand.NewPCG(seed, uint64(k))), reached: reached}
	g.signals = g.r.IntN(3) + 1
	fallible, needs := g.r.IntN(3) == 0, g.r.IntN(4) == 0
	var b strings.Builder
	b.WriteString("import machines\n\n")
	states := g.r.IntN(3) + 1
	reached[fmt.Sprintf("%d variants", states)] = true
	for i := range states {
		g.states = append(g.states, fmt.Sprintf("S%d", i))
		f := []string{"", "", "t", "xs"}[g.r.IntN(4)]
		g.fields = append(g.fields, f)
		switch f {
		case "t":
			fmt.Fprintf(&b, "type S%d { n int, t string }\n", i)
		case "xs":
			fmt.Fprintf(&b, "type S%d { n int, xs []int }\n", i)
		default:
			fmt.Fprintf(&b, "type S%d { n int }\n", i)
		}
	}
	b.WriteString("type St = " + strings.Join(g.states, " | ") + "\n")
	var sigs []string
	for j := range g.signals {
		sigs = append(sigs, fmt.Sprintf("E%d", j))
		fmt.Fprintf(&b, "type E%d { v int }\n", j)
	}
	b.WriteString("type Sig = " + strings.Join(sigs, " | ") + "\ntype Boom {}\n\n")
	rows, runRows := "", ""
	if fallible {
		reached["fails"] = true
		rows += " fails Boom"
	}
	if needs {
		reached["needs"] = true
		rows += " needs Probe"
		runRows = " needs Probe"
		b.WriteString("type Probe { Tick() }\ntype Quiet {}\n\nfunc Tick(_ Quiet) {}\n\n")
	}
	last := len(g.states) - 1
	b.WriteString("func num(s St) int {\n\tmatch s {\n")
	for i, st := range g.states {
		fmt.Fprintf(&b, "\tcase %s as a:\n\t\ta.n * 4 + %d\n", st, i)
	}
	b.WriteString("\t}\n}\n\n")
	fmt.Fprintf(&b, "func pick(n int) St {\n\tif n %% 2 == 0 {\n\t\t%s\n\t} else {\n\t\t%s\n\t}\n}\n\n", g.stateLit(0, "n"), g.stateLit(last, "n - 1"))
	fmt.Fprintf(&b, "func deciding(n int) Decision[St, int] {\n\tif n > 0 {\n\t\tStay[St]{Next: %s}\n\t} else {\n\t\tEmit[St, int]{Next: %s, Output: n}\n\t}\n}\n\n", g.stateLit(last, "n"), g.stateLit(0, "n + 1"))
	fmt.Fprintf(&b, "func step(s St, e Sig) Decision[St, int]%s {\n", rows)
	if needs {
		b.WriteString("\tProbe.Tick()\n")
	}
	wild := last > 0 && g.r.IntN(3) == 0
	covered := len(g.states)
	if wild {
		reached["wildcard"] = true
		covered--
	}
	switch {
	case g.r.IntN(2) == 0:
		reached["simple match"] = true
		b.WriteString("\tmatch s {\n")
		for i := range covered {
			fmt.Fprintf(&b, "\tcase %s as a:\n\t\tmatch e {\n", g.states[i])
			for j := range g.signals {
				fmt.Fprintf(&b, "\t\tcase E%d as x:\n%s", j, g.arm(i, "\t\t\t", fallible))
			}
			b.WriteString("\t\t}\n")
		}
		if wild {
			b.WriteString("\tcase _:\n\t\tIgnore\n")
		}
		b.WriteString("\t}\n")
	default:
		reached["tree match"] = true
		b.WriteString("\tmatch (s, e) {\n")
		for i := range covered {
			for j := range g.signals {
				fmt.Fprintf(&b, "\tcase (%s as a, E%d as x):\n%s", g.states[i], j, g.arm(i, "\t\t", fallible))
			}
		}
		if wild {
			b.WriteString("\tcase (_, _):\n\t\tIgnore\n")
		}
		b.WriteString("\t}\n")
	}
	b.WriteString("}\n\n")
	fmt.Fprintf(&b, "type M machine[St, Sig] {\n\tinitial: %s,\n\tstep:    step,\n}\n\n", g.stateLit(0, "1"))
	b.WriteString("func show(s St) string {\n\tmatch s {\n")
	for i, st := range g.states {
		switch g.fields[i] {
		case "t":
			fmt.Fprintf(&b, "\tcase %s as a:\n\t\t\"%s \\(a.n) \\(a.t)\"\n", st, st)
		case "xs":
			fmt.Fprintf(&b, "\tcase %s as a:\n\t\t\"%s \\(a.n) \\(len(a.xs))\"\n", st, st)
		default:
			fmt.Fprintf(&b, "\tcase %s as a:\n\t\t\"%s \\(a.n)\"\n", st, st)
		}
	}
	b.WriteString("\t}\n}\n\n")
	catch := "\tcase machines.Rejected as r:\n\t\treturn \"rejected \\(r.At)\"\n"
	if fallible {
		catch += "\tcase Boom:\n\t\treturn \"boom\"\n"
	}
	b.WriteString(`func ints(xs []int) string {
	let mut t = "["
	for i, x in xs {
		if i > 0 {
			t = t + " "
		}
		t = t + "\(x)"
	}
	t + "]"
}

func told(o machines.Outcome[St, int]) string {
	match o {
	case machines.Finished[St, int] as f:
		"finished \(show(f.State)) \(f.Output) \(ints(f.Emitted))"
	case machines.Running[St, int] as r:
		"running \(show(r.State)) \(ints(r.Emitted))"
	}
}

`)
	fmt.Fprintf(&b, "func run(signals []Sig) string%s {\n\tlet o = machines.Run(M{}, signals) catch {\n%s\t}\n\ttold(o)\n}\n\n", runRows, catch)
	fmt.Fprintf(&b, "func from(s St, signals []Sig) string%s {\n\tlet o = machines.From(M{}, s, signals) catch {\n%s\t}\n\ttold(o)\n}\n\n", runRows, catch)
	fmt.Fprintf(&b, "func timed(signals []Sig) string%s {\n\tlet mut trace = []machines.Timed[Sig]{}\n\tfor e in signals {\n\t\ttrace = trace.Append(machines.Timed[Sig]{At: 0s, Signal: e})\n\t}\n\tlet o = machines.RunTimed(M{}, trace, 0s) catch {\n%s\t}\n\ttold(o)\n}\n\n", runRows, catch)
	b.WriteString("func main() needs Console {\n")
	ind := "\t"
	if needs {
		b.WriteString("\twith context { Probe: Quiet } {\n")
		ind = "\t\t"
	}
	for l := range 3 {
		n := g.r.IntN(8) + 1
		var lit []string
		for range n {
			lit = append(lit, fmt.Sprintf("E%d{v: %d}", g.r.IntN(g.signals), g.r.IntN(9)-4))
		}
		fmt.Fprintf(&b, "%slet xs%d = []Sig{%s}\n", ind, l, strings.Join(lit, ", "))
		fmt.Fprintf(&b, "%sfor i in 0..%d {\n%s\tConsole.Println(\"\\(run(xs%d[0:i])) | \\(timed(xs%d[0:i]))\")\n%s}\n", ind, n+1, ind, l, l, ind)
		fmt.Fprintf(&b, "%sConsole.Println(from(%s, xs%d))\n", ind, g.stateLit(g.r.IntN(len(g.states)), fmt.Sprint(g.r.IntN(9)-4)), l)
	}
	// Every arm runs: one signal of each variant (both signs of v, for
	// the arms that branch on it) from each state.
	for i := range g.states {
		for j := range g.signals {
			for _, v := range []string{"1", "-1"} {
				fmt.Fprintf(&b, "%sConsole.Println(from(%s, []Sig{E%d{v: %s}}))\n", ind, g.stateLit(i, "2"), j, v)
			}
		}
	}
	if needs {
		b.WriteString("\t}\n")
	}
	b.WriteString("}\n")
	return fmt.Sprintf("flatrun%d_%02d", seed, k), b.String(), !fallible
}

// flatRunPaths are the paths the fixed seeds must reach.
var flatRunPaths = []string{
	"1 variants", "2 variants", "3 variants", "string field", "slice field", "next s", "state read", "next bound", "next call", "next variant",
	"literal", "emit", "done", "union call", "bare", "flat reject", "let variant", "let union", "return", "if", "fail",
	"simple match", "tree match", "wildcard", "needs", "fails",
}

// lowerMachineMode lowers src with the flat run, or with std's loop when
// ref is set (machineLowering "interface").
func lowerMachineMode(t *testing.T, name, src string, ref bool) *lowered {
	t.Helper()
	saved := machineLowering
	defer func() { machineLowering = saved }()
	machineLowering = ""
	if ref {
		machineLowering = "interface"
	}
	return mustLower(t, name, src)
}

// flatRunCase is one generated program, lowered both ways.
type flatRunCase struct {
	seed      uint64
	name, src string
	flat, ref *lowered
}

// flatRunCases generates programs for seeds until every path is reached
// (with the fixed seeds), 8 per seed at least and 32 at most.
func flatRunCases(t *testing.T, seeds []uint64, check bool) []flatRunCase {
	t.Helper()
	reached := map[string]bool{}
	missing := func() string {
		for _, p := range flatRunPaths {
			if !reached[p] {
				return p
			}
		}
		return ""
	}
	var cases []flatRunCase
	for _, seed := range seeds {
		for k := 0; k < 8 || check && missing() != "" && k < 32; k++ {
			name, src, flat := flatRunProgram(seed, k, reached)
			c := flatRunCase{seed: seed, name: name, src: src, flat: lowerMachineMode(t, name, src, false), ref: lowerMachineMode(t, name+"ref", src, true)}
			// The flat run is taken exactly where the step has no error
			// row, and the reference lowering never takes it.
			if got := strings.Contains(c.flat.mainGo(), "stepFlatM("); got != flat {
				t.Errorf("seed %d, %s: the flat run is taken %v, want %v\n%s", seed, name, got, flat, src)
			}
			if strings.Contains(c.ref.mainGo(), "stepFlat") {
				t.Errorf("seed %d, %s: the reference lowering takes the flat run\n%s", seed, name, src)
			}
			cases = append(cases, c)
		}
	}
	if check {
		if m := missing(); m != "" {
			t.Fatalf("seeds %v never reach %q: %v", seeds, m, reached)
		}
	}
	return cases
}

// flatRunDiffers reports how the outputs of c's two programs break the
// property, or "".
func flatRunDiffers(flat, ref string, flatCode, refCode int) string {
	if flatCode != 0 || refCode != 0 {
		return fmt.Sprintf("exit %d (flat) and %d (reference)", flatCode, refCode)
	}
	for _, line := range lines(flat) {
		if run, timed, ok := strings.Cut(line, " | "); ok && run != timed {
			return "machines.Run and machines.RunTimed differ: " + line
		}
	}
	if flat != ref {
		return "the flat and the reference lowering print different lines"
	}
	return ""
}

// TestMachineFlatRunAgrees is the seeded property (see above).
//
// It builds and runs the programs because the behaviour under test is the
// lowered Go (the flat step's returns, the tag switch and the loop), and
// the repository has no reference interpreter of Ergo; RunTimed and the
// reference lowering are its references.
func TestMachineFlatRunAgrees(t *testing.T) {
	seeds := []uint64{1, 2}
	if *flatRunSeed != 0 {
		seeds = []uint64{*flatRunSeed}
	}
	cases := flatRunCases(t, seeds, *flatRunSeed == 0)
	var builds []*lowered
	for _, c := range cases {
		builds = append(builds, c.flat, c.ref)
	}
	exes := buildAll(t, builds, true)
	if exes == nil {
		t.Fatalf("the flat run programs do not build (seeds %v)", seeds)
	}
	rejected := false
	for _, c := range cases {
		flat, flatErr, flatCode := runExe(t, exes[c.name])
		ref, refErr, refCode := runExe(t, exes[c.name+"ref"])
		if why := flatRunDiffers(flat, ref, flatCode, refCode); why != "" {
			t.Errorf("seed %d, %s: %s\nReplay: go test -run '^TestMachineFlatRunAgrees$' ./internal/sketch/lower -args -flatrun.seed=%d\n%s\n--- flat\n%s%s\n--- reference\n%s%s", c.seed, c.name, why, c.seed, c.src, flat, firstLines(flatErr, 8), ref, firstLines(refErr, 8))
		}
		if strings.Contains(c.flat.mainGo(), "stepFlatM(") && strings.Contains(flat, "rejected ") {
			rejected = true
		}
	}
	// The flat loop's Reject (the index it reports) is reached at run
	// time, not only written: the mutant "loop" needs a run that rejects.
	if *flatRunSeed == 0 && !rejected {
		t.Errorf("seeds %v: no flat run rejects a signal", seeds)
	}
	t.Logf("seeds %v: %d programs", seeds, len(cases))
}

var machineMutants = flag.Bool("machine.mutants", false, "run TestMachineFlatRunMutants")

// TestMachineFlatRunMutants breaks each helper of the flat run in turn
// (machineMutant) and requires the property's programs to show it: each
// helper has a guard that fails without it. It builds eight sets of
// programs, so it runs only with -machine.mutants:
// go test -run '^TestMachineFlatRunMutants$' ./internal/sketch/lower -args -machine.mutants
func TestMachineFlatRunMutants(t *testing.T) {
	if !*machineMutants {
		t.Skip("run with -machine.mutants")
	}
	for _, m := range []string{"tag", "payload", "out", "in", "decide", "union", "loop", "tagswitch"} {
		t.Run(m, func(t *testing.T) {
			saved := machineMutant
			machineMutant = m
			defer func() { machineMutant = saved }()
			cases := flatRunCases(t, []uint64{1, 2}, true)
			var builds []*lowered
			for _, c := range cases {
				builds = append(builds, c.flat, c.ref)
			}
			exes := buildAll(t, builds, true)
			if exes == nil {
				t.Fatal("the mutant's programs do not build")
			}
			var killed []string
			for _, c := range cases {
				flat, _, flatCode := runExe(t, exes[c.name])
				ref, _, refCode := runExe(t, exes[c.name+"ref"])
				if flatRunDiffers(flat, ref, flatCode, refCode) != "" {
					killed = append(killed, c.name)
				}
			}
			if len(killed) == 0 {
				t.Errorf("mutant %s survives every program", m)
			}
			t.Logf("mutant %s killed by %d of %d programs", m, len(killed), len(cases))
		})
	}
	t.Run("carry-union", func(t *testing.T) {
		saved := machineMutant
		machineMutant = "carry-union"
		defer func() { machineMutant = saved }()
		if short, long := flatRunAllocs(t); short == long {
			t.Errorf("mutant carry-union survives: a run of 10 signals allocates %v, of 1000 %v", short, long)
		}
	})
}

// flatAllocSource is a machine of two fieldful variants with no slice and
// no Map whose step never emits: a run's allocations are its outcome's
// alone, whatever its length. The values are past 255, where Go boxes an
// int without its static table.
const flatAllocSource = `import machines

type Idle { n int }
type Busy { n int, k int }
type St = Idle | Busy
type Tick { v int }
type Stop {}
type Sig = Tick | Stop

func step(s St, e Sig) Decision[St, int] {
	match (s, e) {
	case (Idle as a, Tick as t):
		Enter[St]{Next: Busy{n: a.n + t.v, k: 1}}
	case (Busy as b, Tick as t):
		if b.k > 3 {
			Enter[St]{Next: Idle{n: b.n}}
		} else {
			Stay[St]{Next: Busy{n: b.n + t.v, k: b.k + 1}}
		}
	case (_, Stop):
		Stay[St]{Next: s}
	}
}

type M machine[St, Sig] {
	initial: Idle{n: 300},
	step:    step,
}

func main() needs Console {
	let o = machines.Run(M{}, []Sig{Tick{v: 1}, Stop}) catch {
	case machines.Rejected:
		return
	}
	match o {
	case machines.Running[St, int]:
		Console.Println("running")
	case machines.Finished[St, int]:
		Console.Println("finished")
	}
}
`

// flatAllocDriver runs the flat run of flatAllocSource over 10 and over
// 1000 signals and logs the allocations of each run.
const flatAllocDriver = `package main

import (
	"testing"

	rt "ergo.local/prototype/runtime/rt"
)

func TestFlatRunAllocs(t *testing.T) {
	env := &Env{task: rt.Root("allocs")}
	ergoInit(env)
	sigs := func(n int) []Sig {
		xs := make([]Sig, n)
		for i := range xs {
			if i%5 == 4 {
				xs[i] = Stop{}
			} else {
				xs[i] = Tick{v: 300 + i}
			}
		}
		return xs
	}
	short, long := sigs(10), sigs(1000)
	a := testing.AllocsPerRun(50, func() { _, _ = ergoRunM(env, M{}.Initial(), short) })
	b := testing.AllocsPerRun(50, func() { _, _ = ergoRunM(env, M{}.Initial(), long) })
	t.Logf("ALLOCS %v %v", a, b)
}
`

// flatRunAllocs is the allocations of a flat run of 10 and of 1000
// signals of flatAllocSource.
func flatRunAllocs(t *testing.T) (short, long float64) {
	t.Helper()
	out := goTestEmittedVerbose(t, lowerMachineMode(t, "flatallocs", flatAllocSource, false), flatAllocDriver)
	_, rest, ok := strings.Cut(out, "ALLOCS ")
	if !ok {
		t.Fatalf("no ALLOCS line in\n%s", out)
	}
	if _, err := fmt.Sscan(rest, &short, &long); err != nil {
		t.Fatalf("the ALLOCS line: %v\n%s", err, out)
	}
	return short, long
}

// TestMachineFlatRunAllocates: a flat run's state lives in the loop as a
// Go value, so a step of fieldful variants with no slice allocates
// nothing: a run of 1000 signals allocates what a run of 10 does. It
// builds the emitted code with a Go test beside it, because allocation is
// a fact of Go's escape analysis of the lowered code, which no in-process
// check sees. Mutant: carry-union (TestMachineFlatRunMutants), which the
// property cannot see, since it changes no output.
func TestMachineFlatRunAllocates(t *testing.T) {
	short, long := flatRunAllocs(t)
	if short != long {
		t.Errorf("a flat run of 10 signals allocates %v times, of 1000 %v times: the step allocates", short, long)
	}
	t.Logf("allocations per run: %v (10 signals), %v (1000 signals)", short, long)
}
