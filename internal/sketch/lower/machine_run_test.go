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
// text the program prints, with the first signal's index first and the
// outputs before emitted earlier; it returns the outputs after the fold.
func foldRun(table [][]runCell, s runState, signals []runSignal, first int, before []int) (string, runState, bool, []int) {
	out := slices.Clone(before)
	for i, e := range signals {
		c := table[s.v][e.v]
		switch c.kind {
		case "enter", "stay":
			s = runState{c.target, s.n + e.x + c.delta}
		case "emit":
			out = append(out, s.n+e.x)
			s = runState{c.target, s.n + e.x + c.delta}
		case "ignore":
		case "reject":
			return "rejected " + strconv.Itoa(i+first), s, false, out
		case "done":
			return fmt.Sprintf("finished %s %d %s", s.text(), s.n+e.x, intsText(out)), s, false, out
		case "fail":
			return "boom", s, false, out
		}
	}
	return "running " + s.text() + " " + intsText(out), s, true, out
}

// intsText is the text the programs' ints prints for xs.
func intsText(xs []int) string {
	parts := make([]string, len(xs))
	for i, x := range xs {
		parts[i] = strconv.Itoa(x)
	}
	return "[" + strings.Join(parts, " ") + "]"
}

// runTimer is a state variant's timer in a generated machine: none, or a
// signal variant sig with v x, In base + (n mod 3) nanoseconds after the
// entry opens, so the duration is computed from the state.
type runTimer struct {
	has          bool
	base, sig, x int
}

// timedSignal is a signal of a timed trace at the time at (nanoseconds).
type timedSignal struct {
	at int
	e  runSignal
}

// foldTimed is the reference of machines.RunTimed: a discrete-event model
// with one timer slot. The slot is armed when an entry opens (the first
// state, an Enter or an Emit) and emptied when it fires or when the next
// entry opens; Stay, Ignore and a timer's Reject leave it. A timer fires
// before a signal later than its deadline, after one at the same time,
// and by end after the last signal. seen records the cases the run
// reached, for the generator's coverage check.
func foldTimed(table [][]runCell, timers []runTimer, s runState, trace []timedSignal, end int, seen map[string]bool) string {
	var out []int
	now, deadline, armed := 0, 0, false
	var due runSignal
	// opener is what opened the entry: "first", "enter", "emit", and a
	// " late" suffix when a signal earlier than the clock opened it.
	opener := "first"
	// stale are the deadlines of timers whose entries ended unfired.
	var stale []int
	open := func(by string) {
		if armed {
			seen["entry ended before its timer"] = true
			stale = append(stale, deadline)
		}
		armed, opener = false, by
		if tm := timers[s.v]; tm.has {
			armed, deadline, due = true, now+tm.base+((s.n%3)+3)%3, runSignal{tm.sig, tm.x}
		}
	}
	open("first")
	stayed := false
	for i := 0; ; {
		var e runSignal
		timer, late := false, false
		switch {
		case armed && (i < len(trace) && deadline < trace[i].at || i >= len(trace) && deadline <= end):
			if i < len(trace) && deadline == trace[i].at-1 {
				seen["timer just before a signal"] = true
			}
			now = max(now, deadline)
			e, timer, armed = due, true, false
			seen["timer fired"] = true
			seen["timer fired in an entry opened by "+opener] = true
			if stayed {
				seen["timer fired after a stay"] = true
			}
		case i < len(trace):
			if armed && deadline == trace[i].at {
				seen["signal at its timer's deadline"] = true
			}
			late = trace[i].at < now
			now = max(now, trace[i].at)
			e = trace[i].e
			i++
		default:
			return "running " + s.text() + " " + intsText(out)
		}
		for len(stale) > 0 && slices.Min(stale) <= now {
			seen["a stale timer came due"] = true
			stale = slices.DeleteFunc(stale, func(d int) bool { return d <= now })
		}
		c := table[s.v][e.v]
		switch c.kind {
		case "enter", "emit":
			if c.kind == "emit" {
				out = append(out, s.n+e.x)
			}
			s = runState{c.target, s.n + e.x + c.delta}
			stayed = false
			by := c.kind
			if late {
				by += " late"
			}
			open(by)
		case "stay":
			s = runState{c.target, s.n + e.x + c.delta}
			stayed = stayed || armed
		case "ignore":
		case "reject":
			if !timer {
				return "rejected " + strconv.Itoa(i-1)
			}
			seen["timer rejected"] = true
		case "done":
			return fmt.Sprintf("finished %s %d %s", s.text(), s.n+e.x, intsText(out))
		case "fail":
			return "boom"
		}
	}
}

// genRunProgram is one machine with 1-3 state variants and 1-3 signal
// variants, each with an int field, a step table over every decision, a
// typed failure included, and a timer on some state variants (its
// after entry, in half the programs); its step needs a capability, Probe,
// in half the programs. main prints the initial state and, for each
// generated signal list, the run over the list, the run over its first
// half and then, when that is Running, from its state over the second
// half, and the timed run of the list at generated times.
func genRunProgram(r *rand.Rand, k int, seen map[string]bool) runProgram {
	ns, ne := r.Intn(3)+1, r.Intn(3)+1
	table := make([][]runCell, ns)
	for i := range table {
		table[i] = make([]runCell, ne)
		for j := range table[i] {
			// Rejects, Dones and failures end a run: they are drawn less
			// often, so runs get long.
			kind := []string{"enter", "stay", "emit", "ignore"}[r.Intn(4)]
			if r.Intn(5) == 0 {
				kind = []string{"reject", "done", "fail"}[r.Intn(3)]
			}
			table[i][j] = runCell{kind: kind, target: r.Intn(ns), delta: r.Intn(7) - 3}
		}
	}
	timed := r.Intn(2) == 0
	timers := make([]runTimer, ns)
	for i := range timers {
		if timed && r.Intn(3) > 0 {
			timers[i] = runTimer{has: true, base: r.Intn(4) + 1, sig: r.Intn(ne), x: r.Intn(7) - 3}
			if r.Intn(4) == 0 {
				// A timer's signal that the state rejects: no one asked.
				table[i][timers[i].sig].kind = "reject"
			}
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
			case "emit":
				fmt.Fprintf(&b, "Emit[St, int]{Next: S%d{n: a.n + x.v + %d}, Output: a.n + x.v}", c.target, c.delta)
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
	after := ""
	if timed {
		after = "\tafter:   timer,\n"
		b.WriteString("func timer(s St) Option[Timeout[Sig]] {\n\tmatch s {\n")
		for i, tm := range timers {
			if !tm.has {
				fmt.Fprintf(&b, "\tcase S%d:\n\t\tNone\n", i)
				continue
			}
			fmt.Fprintf(&b, "\tcase S%d as a:\n\t\tSome[Timeout[Sig]]{Value: Timeout[Sig]{In: duration(int64(%d + ((a.n %% 3) + 3) %% 3)), Signal: E%d{v: %d}}}\n", i, tm.base, tm.sig, tm.x)
		}
		b.WriteString("\t}\n}\n\n")
	}
	init := runState{r.Intn(ns), r.Intn(9) - 4}
	fmt.Fprintf(&b, "type M machine[St, Sig] {\n\tinitial: S%d{n: %d},\n\tstep:    step,\n%s}\n\n", init.v, init.n, after)
	b.WriteString("func show(s St) string {\n\tmatch s {\n")
	for i := range ns {
		fmt.Fprintf(&b, "\tcase S%d as a:\n\t\t\"S%d \\(a.n)\"\n", i, i)
	}
	b.WriteString("\t}\n}\n\n")
	b.WriteString(strings.ReplaceAll(`func ints(xs []int) string {
	let mut t = "["
	for i, x in xs {
		if i > 0 {
			t = t + " "
		}
		t = t + "\(x)"
	}
	t + "]"
}

func cat(a []int, b []int) []int {
	let mut all = a
	for x in b {
		all = all.Append(x)
	}
	all
}

func told(o machines.Outcome[St, int], before []int) string {
	match o {
	case machines.Finished[St, int] as f:
		"finished \(show(f.State)) \(f.Output) \(ints(cat(before, f.Emitted)))"
	case machines.Running[St, int] as r:
		"running \(show(r.State)) \(ints(cat(before, r.Emitted)))"
	}
}

func run(signals []Sig) string NEEDS {
	let o = machines.Run(M{}, signals) catch {
	case machines.Rejected as r:
		return "rejected \(r.At)"
	case Boom:
		return "boom"
	}
	told(o, []int{})
}

func from(s St, signals []Sig, first int, before []int) string NEEDS {
	let o = machines.From(M{}, s, signals) catch {
	case machines.Rejected as r:
		return "rejected \(r.At + first)"
	case Boom:
		return "boom"
	}
	told(o, before)
}

func split(a []Sig, b []Sig) string NEEDS {
	let o = machines.Run(M{}, a) catch {
	case machines.Rejected:
		return "-"
	case Boom:
		return "-"
	}
	match o {
	case machines.Running[St, int] as r:
		from(r.State, b, len(a), r.Emitted)
	case machines.Finished[St, int]:
		"-"
	}
}

func timed(trace []machines.Timed[Sig], end int) string NEEDS {
	let o = machines.RunTimed(M{}, trace, duration(int64(end))) catch {
	case machines.Rejected as r:
		return "rejected \(r.At)"
	case Boom:
		return "boom"
	}
	told(o, []int{})
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
		trace := make([]timedSignal, n)
		var lit, tlit []string
		at := 0
		for i := range signals {
			signals[i] = runSignal{r.Intn(ne), r.Intn(7) - 3}
			// Times mostly rise; one in six is a tie and one in six goes
			// back, which the run's clock never does.
			switch r.Intn(6) {
			case 0:
				at -= r.Intn(4) + 1
			case 1:
			default:
				at += r.Intn(5) + 1
			}
			trace[i] = timedSignal{at, signals[i]}
			lit = append(lit, fmt.Sprintf("E%d{v: %d}", signals[i].v, signals[i].x))
			tlit = append(tlit, fmt.Sprintf("machines.Timed[Sig]{At: duration(int64(%d)), Signal: E%d{v: %d}}", at, signals[i].v, signals[i].x))
		}
		end := at + r.Intn(8)
		h := n / 2
		fmt.Fprintf(&b, "\tlet xs%d = []Sig{%s}\n\tConsole.Println(run(xs%d))\n\tConsole.Println(split(xs%d[0:%d], xs%d[%d:%d]))\n", l, strings.Join(lit, ", "), l, l, h, l, h, n)
		fmt.Fprintf(&b, "\tConsole.Println(timed([]machines.Timed[Sig]{%s}, %d))\n", strings.Join(tlit, ", "), end)
		whole, _, _, _ := foldRun(table, init, signals, 0, nil)
		want = append(want, whole)
		if _, at, running, out := foldRun(table, init, signals[:h], 0, nil); running {
			rest, _, _, _ := foldRun(table, at, signals[h:], h, out)
			want = append(want, rest)
		} else {
			want = append(want, "-")
		}
		want = append(want, foldTimed(table, timers, init, trace, end, seen))
	}
	if needs {
		b.WriteString("\t}\n")
	}
	b.WriteString("}\n")
	return runProgram{name: fmt.Sprintf("machinerun_%02d", k), src: b.String(), want: want}
}

// TestMachineRunAgreesWithFold is the acceptance property: over generated
// step tables, timers and signal lists, machines.Run, machines.From and
// machines.Initial print what the reference fold computes, Emit outputs
// in order; the split law (Run over a ++ b is From over b after a) holds
// on every list; and machines.RunTimed over the list at generated times
// prints what the discrete-event model foldTimed computes (V5: a timer
// never fires after its entry ended, Stay keeps it, Enter and Emit
// restart it, a signal at a timer's deadline goes first). A failure
// prints the program. ERGO_MACHINERUN_SEED replays another seed.
//
// It builds and runs the programs because the behaviour under test is the
// lowered Go: the ConvMachine value (plan Initial, the step's and the
// after's function values) and std machines' Ergo body run only there,
// and the repository has no reference interpreter of Ergo. The std laws
// of machines (over a Machine record with no timer) miss what it catches:
// Ignore treated as Reject, the rejected index, Done's state, inference
// through a machine value, and every timer rule.
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
	kinds, seen := map[string]bool{}, map[string]bool{}
	// The generated runs must reach every way a run ends, an Emit's
	// output, and every timer case of the model: programs are generated
	// until they do, 16 at least and 128 at most.
	ends := []string{"running", "finished", "rejected", "boom", "-", "emitted"}
	cases := []string{"timer fired after a stay", "a stale timer came due", "signal at its timer's deadline", "timer just before a signal", "timer rejected",
		"timer fired in an entry opened by first", "timer fired in an entry opened by enter", "timer fired in an entry opened by emit", "a timer of an entry a late signal opened fired"}
	missing := func() string {
		seen["a timer of an entry a late signal opened fired"] = seen["timer fired in an entry opened by enter late"] || seen["timer fired in an entry opened by emit late"]
		for _, k := range ends {
			if !kinds[k] {
				return "no generated run ends " + strconv.Quote(k)
			}
		}
		for _, c := range cases {
			if !seen[c] {
				return "no generated timed run reaches " + strconv.Quote(c)
			}
		}
		return ""
	}
	for k := 0; k < 16 || missing() != "" && k < 128; k++ {
		p := genRunProgram(r, k, seen)
		progs = append(progs, p)
		builds = append(builds, mustLower(t, p.name, p.src))
		for _, w := range p.want {
			kinds[strings.SplitN(w, " ", 2)[0]] = true
			if strings.Contains(w, "[") && !strings.Contains(w, "[]") {
				kinds["emitted"] = true
			}
		}
	}
	if m := missing(); m != "" {
		t.Fatalf("seed %d, %d programs: %s", seed, len(progs), m)
	}
	t.Logf("seed %d: %d programs", seed, len(progs))
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

// TestMachineAfterRefusals: a machine's after names a package function
// from its state to Option[Timeout[E]] with no rows, since the runner
// calls it after a commit. Diagnostic rows: they alone catch a checker
// that admits an after of another type or with a row, which no program
// that builds can show.
func TestMachineAfterRefusals(t *testing.T) {
	head := `type St = A
type A {}
type Sig = Go
type Go {}

func step(s St, e Sig) Decision[St, int] { Ignore }

`
	machine := "type M machine[St, Sig] {\n\tinitial: A,\n\tstep:    step,\n\tafter:   timer,\n}\n\nfunc main() {}\n"
	for _, c := range []struct{ name, timer, want string }{
		{"signal", "func timer(e Sig) Option[Timeout[Sig]] { None }\n\n",
			"S108 the after timer of machine M must have the type func(main.St) Option[Timeout[main.Sig]], with no error or capability row: the runner calls it after a commit (runtime.md, Machines and actors 9)"},
		{"row", "func timer(s St) Option[Timeout[Sig]] {\n\tConsole.Println(\"x\")\n\tNone\n}\n\n",
			"S108 cannot use func(main.St) Option[Timeout[main.Sig]] needs Console as func(main.St) Option[Timeout[main.Sig]] in machine M's after: its capability row {Console} is wider than {}"},
		{"unknown", "", "S104 unknown function timer"},
	} {
		var got []string
		for _, d := range lowerSource("machineafter", head+c.timer+machine, true).diags {
			got = append(got, d.String())
		}
		if len(got) != 1 || !strings.Contains(got[0], c.want) {
			t.Errorf("%s: diagnostics %q, want one with %q", c.name, got, c.want)
		}
	}
}

// machinesLawsProbe restates the laws of std machines as tests of the same
// name (the Std laws ledger, docs/spec/stdlib.md).
const machinesLawsProbe = `import machines

func tallyStep(s int, e int) Decision[int, int] {
	match ((e % 6) + 6) % 6 {
	case 0:
		Enter[int]{Next: s + e}
	case 1:
		Stay[int]{Next: s + 1}
	case 2:
		Ignore
	case 3:
		Reject
	case 4:
		Emit[int, int]{Next: s - 1, Output: s}
	case _:
		Done[int]{Output: s}
	}
}

func untimed(s int) Option[Timeout[int]] { None }

func tally() Machine[int, int, int] { Machine[int, int, int]{Initial: 0, Step: tallyStep, After: untimed} }

func ints(xs []int) string {
	let mut t = "["
	for i, x in xs {
		if i > 0 {
			t = t + " "
		}
		t = t + "\(x)"
	}
	t + "]"
}

func cat(a []int, b []int) []int {
	let mut all = a
	for x in b {
		all = all.Append(x)
	}
	all
}

func told(o machines.Outcome[int, int], before []int) string {
	match o {
	case machines.Finished[int, int] as f:
		"finished \(f.State) \(f.Output) \(ints(cat(before, f.Emitted)))"
	case machines.Running[int, int] as r:
		"running \(r.State) \(ints(cat(before, r.Emitted)))"
	}
}

func from(s int, signals []int, first int, before []int) string {
	let o = machines.From(tally(), s, signals) catch {
	case machines.Rejected as r:
		return "rejected \(r.At + first)"
	}
	told(o, before)
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
	case machines.Running[int, int] as r:
		from(0, whole, 0, []int{}) == from(r.State, b, len(a), r.Emitted)
	case machines.Finished[int, int]:
		true
	}
}

func NextTakesItsState(s int, n int) bool {
	machines.Next[int, int](s, Enter[int]{Next: n}) == n && machines.Next[int, int](s, Stay[int]{Next: n}) == n &&
		machines.Next[int, int](s, Emit[int, int]{Next: n, Output: s}) == n && machines.Next[int, int](s, Ignore) == s &&
		machines.Next[int, int](s, Reject) == s && machines.Next[int, int](s, Done[int]{Output: n}) == s
}

func UntimedRunsAsRun(signals []int, gaps []int) bool {
	let mut trace = []machines.Timed[int]{}
	let mut at = 0s
	for i, e in signals {
		if i < len(gaps) {
			at = at + duration(int64(gaps[i] % 1000))
		}
		trace = trace.Append(machines.Timed[int]{At: at, Signal: e})
	}
	let timed = machines.RunTimed(tally(), trace, at) catch {
	case machines.Rejected as r:
		return from(0, signals, 0, []int{}) == "rejected \(r.At)"
	}
	from(0, signals, 0, []int{}) == told(timed, []int{})
}

test MachinesRunSplits(a []int, b []int) { assert RunSplits(a, b) }

test MachinesNextTakesItsState(s int, n int) { assert NextTakesItsState(s, n) }

test MachinesUntimedRunsAsRun(signals []int, gaps []int) { assert UntimedRunsAsRun(signals, gaps) }
`

// machinesLawStatements is the statement of each machines law, as the
// ledger writes it.
var machinesLawStatements = map[string]string{
	"machines RunSplits":         `RunSplits(a []int, b []int) { let mut whole = a for e in b { whole = whole.Append(e) } let first = Run(tally(), a) catch { case Rejected: return true } match first { case Running[int, int] as r: from(0, whole, 0, []int{}) == from(r.State, b, len(a), r.Emitted) case Finished[int, int]: true } }`,
	"machines NextTakesItsState": `NextTakesItsState(s int, n int) { Next[int, int](s, Enter[int]{Next: n}) == n && Next[int, int](s, Stay[int]{Next: n}) == n && Next[int, int](s, Emit[int, int]{Next: n, Output: s}) == n && Next[int, int](s, Ignore) == s && Next[int, int](s, Reject) == s && Next[int, int](s, Done[int]{Output: n}) == s }`,
	"machines UntimedRunsAsRun":  `UntimedRunsAsRun(signals []int, gaps []int) { let mut trace = []Timed[int]{} let mut at = 0s for i, e in signals { if i < len(gaps) { at = at + duration(int64(gaps[i] % 1000)) } trace = trace.Append(Timed[int]{At: at, Signal: e}) } let timed = RunTimed(tally(), trace, at) catch { case Rejected as r: return from(0, signals, 0, []int{}) == "rejected \(r.At)" } from(0, signals, 0, []int{}) == told(timed, []int{}) }`,
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
