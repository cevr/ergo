package lower

import (
	"fmt"
	"math/rand"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/lawledger"
)

// machine-run (V2; runtime.md, Machines and actors 2 and 8): std machines
// runs a machine value as a pure fold. A machine declaration's value
// converts to the prelude Machine[S, E, O, F, R] (README Gap 46), so
// machines.Run takes any machine, and the step's rows pass through.

// runCell is one cell of a generated step table: what the step decides for
// a state variant and a signal variant.
type runCell struct {
	kind   string // enter, stay, ignore, reject, done, fail
	target int    // the next state's variant (enter, stay)
	delta  int    // added to the next state's n (enter, stay)
}

// runProgram is a generated machine, its signal lists and the lines its
// program must print, which the reference fold below computes.
type runProgram struct {
	name, src string
	want      []string
}

// runState is a state of the reference fold: a variant and its n.
type runState struct{ v, n int }

func (s runState) text() string { return fmt.Sprintf("S%d %d", s.v, s.n) }

// runSignal is a signal of the reference fold: a variant and its v.
type runSignal struct{ v, x int }

// foldRun is the reference: the fold of table from s over signals, in the
// text the program prints, with the first signal's index first.
func foldRun(table [][]runCell, s runState, signals []runSignal, first int) (string, runState, bool) {
	for i, e := range signals {
		c := table[s.v][e.v]
		switch c.kind {
		case "enter", "stay":
			s = runState{c.target, s.n + e.x + c.delta}
		case "ignore":
		case "reject":
			return "rejected " + strconv.Itoa(i+first), s, false
		case "done":
			return fmt.Sprintf("finished %s %d", s.text(), s.n+e.x), s, false
		case "fail":
			return "boom", s, false
		}
	}
	return "running " + s.text(), s, true
}

// genRunProgram is one machine with 1-3 state variants and 1-3 signal
// variants, each with an int field, and a step table over every decision,
// a typed failure included; its step needs a capability, Probe, in half the programs.
// main prints the initial state and, for each generated signal list, the
// run over the list and, when the run over the list's first half is
// Running, the run from its state over the second half.
func genRunProgram(r *rand.Rand, k int) runProgram {
	ns, ne := r.Intn(3)+1, r.Intn(3)+1
	kinds := []string{"enter", "stay", "ignore", "reject", "done", "fail"}
	table := make([][]runCell, ns)
	for i := range table {
		table[i] = make([]runCell, ne)
		for j := range table[i] {
			// Rejects and failures end a run: they are drawn less often, so
			// runs get long.
			kind := kinds[r.Intn(3)]
			if r.Intn(4) == 0 {
				kind = kinds[3+r.Intn(3)]
			}
			table[i][j] = runCell{kind: kind, target: r.Intn(ns), delta: r.Intn(7) - 3}
		}
	}
	needs := r.Intn(2) == 0
	var b strings.Builder
	b.WriteString("import machines\n\n")
	var sv, ev []string
	for i := range ns {
		sv = append(sv, fmt.Sprintf("S%d", i))
		fmt.Fprintf(&b, "type S%d { n int }\n", i)
	}
	for j := range ne {
		ev = append(ev, fmt.Sprintf("E%d", j))
		fmt.Fprintf(&b, "type E%d { v int }\n", j)
	}
	fmt.Fprintf(&b, "type St = %s\ntype Sig = %s\ntype Boom {}\n\n", strings.Join(sv, " | "), strings.Join(ev, " | "))
	row, helperRow := " fails Boom", ""
	if needs {
		row += " needs Probe"
		helperRow = " needs Probe"
		b.WriteString("type Probe { Tick() }\ntype Quiet {}\n\nfunc Tick(_ Quiet) {}\n\n")
	}
	fmt.Fprintf(&b, "func step(s St, e Sig) Decision[St, int]%s {\n", row)
	if needs {
		b.WriteString("\tProbe.Tick()\n")
	}
	b.WriteString("\tmatch s {\n")
	for i := range ns {
		fmt.Fprintf(&b, "\tcase S%d as a:\n\t\tmatch e {\n", i)
		for j := range ne {
			c := table[i][j]
			fmt.Fprintf(&b, "\t\tcase E%d as x:\n\t\t\t", j)
			switch c.kind {
			case "ignore", "reject", "fail":
				// Every arm reads its bindings (S110).
				b.WriteString("let _ = a.n + x.v\n\t\t\t")
			}
			switch c.kind {
			case "enter":
				fmt.Fprintf(&b, "Enter[St]{Next: S%d{n: a.n + x.v + %d}}", c.target, c.delta)
			case "stay":
				fmt.Fprintf(&b, "Stay[St]{Next: S%d{n: a.n + x.v + %d}}", c.target, c.delta)
			case "ignore":
				b.WriteString("Ignore")
			case "reject":
				b.WriteString("Reject")
			case "done":
				b.WriteString("Done[int]{Output: a.n + x.v}")
			case "fail":
				b.WriteString("return Boom")
			}
			b.WriteString("\n")
		}
		b.WriteString("\t\t}\n")
	}
	b.WriteString("\t}\n}\n\n")
	init := runState{r.Intn(ns), r.Intn(9) - 4}
	fmt.Fprintf(&b, "type M machine[St, Sig] {\n\tinitial: S%d{n: %d},\n\tstep:    step,\n}\n\n", init.v, init.n)
	b.WriteString("func show(s St) string {\n\tmatch s {\n")
	for i := range ns {
		fmt.Fprintf(&b, "\tcase S%d as a:\n\t\t\"S%d \\(a.n)\"\n", i, i)
	}
	b.WriteString("\t}\n}\n\n")
	b.WriteString(strings.ReplaceAll(`func told(o machines.Outcome[St, int]) string {
	match o {
	case machines.Finished[St, int] as f:
		"finished \(show(f.State)) \(f.Output)"
	case machines.Running[St] as r:
		"running \(show(r.State))"
	}
}

func run(signals []Sig) string NEEDS {
	let o = machines.Run(M{}, signals) catch {
	case machines.Rejected as r:
		return "rejected \(r.At)"
	case Boom:
		return "boom"
	}
	told(o)
}

func from(s St, signals []Sig, first int) string NEEDS {
	let o = machines.From(M{}, s, signals) catch {
	case machines.Rejected as r:
		return "rejected \(r.At + first)"
	case Boom:
		return "boom"
	}
	told(o)
}

func split(a []Sig, b []Sig) string NEEDS {
	let o = machines.Run(M{}, a) catch {
	case machines.Rejected:
		return "-"
	case Boom:
		return "-"
	}
	match o {
	case machines.Running[St] as r:
		from(r.State, b, len(a))
	case machines.Finished[St, int]:
		"-"
	}
}

`, " NEEDS", helperRow))
	b.WriteString("func main() needs Console {\n\tConsole.Println(show(machines.Initial(M{})))\n")
	if needs {
		b.WriteString("\twith context { Probe: Quiet } {\n")
	}
	want := []string{init.text()}
	for l := range 4 {
		n := r.Intn(9)
		signals := make([]runSignal, n)
		var lit []string
		for i := range signals {
			signals[i] = runSignal{r.Intn(ne), r.Intn(7) - 3}
			lit = append(lit, fmt.Sprintf("E%d{v: %d}", signals[i].v, signals[i].x))
		}
		h := n / 2
		fmt.Fprintf(&b, "\tlet xs%d = []Sig{%s}\n\tConsole.Println(run(xs%d))\n\tConsole.Println(split(xs%d[0:%d], xs%d[%d:%d]))\n", l, strings.Join(lit, ", "), l, l, h, l, h, n)
		whole, _, _ := foldRun(table, init, signals, 0)
		want = append(want, whole)
		if _, at, running := foldRun(table, init, signals[:h], 0); running {
			rest, _, _ := foldRun(table, at, signals[h:], h)
			want = append(want, rest)
		} else {
			want = append(want, "-")
		}
	}
	if needs {
		b.WriteString("\t}\n")
	}
	b.WriteString("}\n")
	return runProgram{name: fmt.Sprintf("machinerun_%02d", k), src: b.String(), want: want}
}

// TestMachineRunAgreesWithFold is the acceptance property: over generated
// step tables and signal lists, machines.Run, machines.From and
// machines.Initial print what the reference fold computes; the split law
// (Run over a ++ b is From over b after a) holds on every list. A failure
// prints the program. ERGO_MACHINERUN_SEED replays another seed.
//
// It builds and runs the programs because the behaviour under test is the
// lowered Go: the ConvMachine value (plan Initial and the step's function
// value) and std machines' Ergo body run only there, and the repository
// has no reference interpreter of Ergo. The std laws of machines (over a
// Machine record) miss what it catches: Ignore treated as Reject, the
// rejected index, Done's state, and inference through a machine value.
func TestMachineRunAgreesWithFold(t *testing.T) {
	seed := int64(20)
	if s := os.Getenv("ERGO_MACHINERUN_SEED"); s != "" {
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			t.Fatalf("ERGO_MACHINERUN_SEED: %v", err)
		}
		seed = n
	}
	r := rand.New(rand.NewSource(seed))
	var progs []runProgram
	var builds []*lowered
	kinds := map[string]bool{}
	for k := 0; k < 16; k++ {
		p := genRunProgram(r, k)
		progs = append(progs, p)
		builds = append(builds, mustLower(t, p.name, p.src))
		for _, w := range p.want {
			kinds[strings.SplitN(w, " ", 2)[0]] = true
		}
	}
	// The generated runs reach every way a run ends.
	for _, k := range []string{"running", "finished", "rejected", "boom", "-"} {
		if !kinds[k] {
			t.Fatalf("seed %d: no generated run ends %q", seed, k)
		}
	}
	exes := buildAll(t, builds, true)
	if exes == nil {
		return
	}
	for _, p := range progs {
		stdout, stderr, code := runExe(t, exes[p.name])
		if got := lines(stdout); code != 0 || !slices.Equal(got, p.want) {
			t.Errorf("seed %d, %s: exit %d\nstdout:\n%s\nwant:\n%s\nstderr:\n%s\nsource:\n%s", seed, p.name, code, stdout, strings.Join(p.want, "\n"), firstLines(stderr, 8), p.src)
		}
	}
}

// TestMachineValueConverts: a machine declaration's value is assignable to
// the prelude Machine of its state, signal and step, and to no other
// instance; a mismatch names the machine's own Machine. A diagnostic row:
// it alone catches a conversion that admits any Machine instance.
func TestMachineValueConverts(t *testing.T) {
	src := `type St = A
type A {}
type Sig = Go
type Go {}
type Boom {}

func step(s St, e Sig) Decision[St, int] fails Boom { Ignore }

type M machine[St, Sig] {
	initial: A,
	step:    step,
}

func main() {
	let _ Machine[St, Sig, string] = M{}
}
`
	var got []string
	for _, d := range lowerSource("machineconv", src, true).diags {
		got = append(got, d.String())
	}
	want := "cannot use main.M as Machine[main.St, main.Sig, string, , ] in let _: its step step gives a Machine with output int, error row {main.Boom} and capability row {}"
	if len(got) != 1 || !strings.Contains(got[0], want) {
		t.Errorf("diagnostics %q, want one with %q and the machine's own Machine", got, want)
	}
}

// TestMachineRunChargesTheStepRows: Run through a machine value has the
// step's rows. run declares none, so it infers them from machines.Run: a
// main that calls it with no provider of the step's capability is S102,
// and the step's failure is not handled there. A diagnostic row: it alone
// catches a Machine view that drops the step's capability row, which the
// generated programs cannot see because their Env holds the provider.
func TestMachineRunChargesTheStepRows(t *testing.T) {
	src := `import machines

type St = A
type A {}
type Sig = Go
type Go {}
type Boom {}
type Probe { Tick() }

func step(s St, e Sig) Decision[St, int] fails Boom needs Probe {
	Probe.Tick()
	Ignore
}

type M machine[St, Sig] {
	initial: A,
	step:    step,
}

func run(xs []Sig) int {
	let _ = try machines.Run(M{}, xs) catch {
	case machines.Rejected:
		return 0
	}
	1
}

func main() {
	let _ = run([]Sig{Go})
}
`
	var got []string
	for _, d := range lowerSource("machinerows", src, true).diags {
		got = append(got, d.String())
	}
	want := []string{
		"29:10: S102 this call requires main.Probe, which is not in the capability row of function main and no with provides it",
		"29:10: S105 the failure {main.Boom} of this expression is not handled: use try, catch or return (rows.md, Error rows 3)",
	}
	if !slices.Equal(got, want) {
		t.Errorf("diagnostics:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// machinesLawsProbe restates the laws of std machines as tests of the same
// name (the Std laws ledger, docs/spec/stdlib.md).
const machinesLawsProbe = `import machines

func tallyStep(s int, e int) Decision[int, int] {
	match ((e % 5) + 5) % 5 {
	case 0:
		Enter[int]{Next: s + e}
	case 1:
		Stay[int]{Next: s + 1}
	case 2:
		Ignore
	case 3:
		Reject
	case _:
		Done[int]{Output: s}
	}
}

func tally() Machine[int, int, int] { Machine[int, int, int]{Initial: 0, Step: tallyStep} }

func told(s int, signals []int, first int) string {
	let o = machines.From(tally(), s, signals) catch {
	case machines.Rejected as r:
		return "rejected \(r.At + first)"
	}
	match o {
	case machines.Finished[int, int] as f:
		"finished \(f.State) \(f.Output)"
	case machines.Running[int] as r:
		"running \(r.State)"
	}
}

func RunSplits(a []int, b []int) bool {
	let mut whole = a
	for e in b {
		whole = whole.Append(e)
	}
	let first = machines.Run(tally(), a) catch {
	case machines.Rejected:
		return true
	}
	match first {
	case machines.Running[int] as r:
		told(0, whole, 0) == told(r.State, b, len(a))
	case machines.Finished[int, int]:
		true
	}
}

func NextTakesItsState(s int, n int) bool {
	machines.Next[int, int](s, Enter[int]{Next: n}) == n && machines.Next[int, int](s, Stay[int]{Next: n}) == n &&
		machines.Next[int, int](s, Ignore) == s && machines.Next[int, int](s, Reject) == s && machines.Next[int, int](s, Done[int]{Output: n}) == s
}

test MachinesRunSplits(a []int, b []int) { assert RunSplits(a, b) }

test MachinesNextTakesItsState(s int, n int) { assert NextTakesItsState(s, n) }
`

// machinesLawStatements is the statement of each machines law, as the
// ledger writes it.
var machinesLawStatements = map[string]string{
	"machines RunSplits":         `RunSplits(a []int, b []int) { let mut whole = a for e in b { whole = whole.Append(e) } let first = Run(tally(), a) catch { case Rejected: return true } match first { case Running[int] as r: told(0, whole, 0) == told(r.State, b, len(a)) case Finished[int, int]: true } }`,
	"machines NextTakesItsState": `NextTakesItsState(s int, n int) { Next[int, int](s, Enter[int]{Next: n}) == n && Next[int, int](s, Stay[int]{Next: n}) == n && Next[int, int](s, Ignore) == s && Next[int, int](s, Reject) == s && Next[int, int](s, Done[int]{Output: n}) == s }`,
}

// TestStdMachinesLawsRun runs the probe's tests with --seed 1; lawledger.Run
// holds that the probe restates exactly the ledger's laws whose runner is
// this test, each with the row's statement, and that each passed. It is
// the ledger runner the Std laws rule requires for each std law (stdlib.md,
// Std laws 4); it builds and runs because the laws run over the lowered
// std body, as ergo test runs them.
func TestStdMachinesLawsRun(t *testing.T) {
	if laws := lawProbe(t, "stdmachines", machinesLawsProbe, machinesLawStatements); laws != nil {
		lawledger.Run(t, laws)
	}
}
