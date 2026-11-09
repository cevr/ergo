package lower

import (
	"bytes"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"ergo.local/prototype/internal/lawledger"
	"ergo.local/prototype/internal/proclimit"
	"ergo.local/prototype/internal/testwait"
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

// machineProgram is a generated machine, its signal lists and the lines its
// program must print, which the reference fold below computes.
type machineProgram struct {
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
// and by end after the last signal. It also gives the run's last instant:
// the time of the step that ended it, or end (or a later signal's time)
// for a run with no Done, which waits until end. seen records the cases
// the run reached, for the generator's coverage check.
func foldTimed(table [][]runCell, timers []runTimer, s runState, trace []timedSignal, end int, seen map[string]bool) (string, int) {
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
			return "running " + s.text() + " " + intsText(out), max(now, end)
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
				return "rejected " + strconv.Itoa(i-1), now
			}
			seen["timer rejected"] = true
		case "done":
			return fmt.Sprintf("finished %s %d %s", s.text(), s.n+e.x, intsText(out)), now
		case "fail":
			return "boom", now
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
func genRunProgram(r *rand.Rand, k int, seen map[string]bool) machineProgram {
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
	b.WriteString("import machines\nimport task\nimport testing\nimport time\n\n")
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
	// The initial state is a value, or (V4) the function start of an input
	// of one int or of two: init.n is then the start's offset.
	init := runState{r.Intn(ns), r.Intn(9) - 4}
	inputs := r.Intn(3)
	seen[fmt.Sprintf("a machine with %d inputs", inputs)] = true
	initial := fmt.Sprintf("S%d{n: %d}", init.v, init.n)
	machineType := "Machine[St, Sig, int, Boom" + strings.ReplaceAll(helperRow, " needs ", ", ") + "]"
	switch inputs {
	case 1:
		fmt.Fprintf(&b, "func start(x int) St { S%d{n: x + %d} }\n\n", init.v, init.n)
		initial = "start"
	case 2:
		fmt.Fprintf(&b, "func start(x int, y int) St { S%d{n: x - y + %d} }\n\n", init.v, init.n)
		initial = "start"
	}
	fmt.Fprintf(&b, "type M machine[St, Sig] {\n\tinitial: %s,\n\tstep:    step,\n%s}\n\n", initial, after)
	b.WriteString("func show(s St) string {\n\tmatch s {\n")
	for i := range ns {
		fmt.Fprintf(&b, "\tcase S%d as a:\n\t\t\"S%d \\(a.n)\"\n", i, i)
	}
	b.WriteString("\t}\n}\n\n")
	b.WriteString(strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(`func ints(xs []int) string {
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

func run(m MT, signals []Sig) string NEEDS {
	let o = machines.Run(m, signals) catch {
	case machines.Rejected as r:
		return "rejected \(r.At)"
	case Boom:
		return "boom"
	}
	told(o, []int{})
}

func from(m MT, s St, signals []Sig, first int, before []int) string NEEDS {
	let o = machines.From(m, s, signals) catch {
	case machines.Rejected as r:
		return "rejected \(r.At + first)"
	case Boom:
		return "boom"
	}
	told(o, before)
}

func split(m MT, a []Sig, b []Sig) string NEEDS {
	let o = machines.Run(m, a) catch {
	case machines.Rejected:
		return "-"
	case Boom:
		return "-"
	}
	match o {
	case machines.Running[St, int] as r:
		from(m, r.State, b, len(a), r.Emitted)
	case machines.Finished[St, int]:
		"-"
	}
}

func timedRun(m MT, trace []machines.Timed[Sig], end int) string CLOCKED {
	let o = machines.RunTimed(m, trace, duration(int64(end))) catch {
	case machines.Rejected as r:
		return "rejected \(r.At)"
	case Boom:
		return "boom"
	}
	told(o, []int{})
}

func sleeper(start time.Time, trace []machines.Timed[Sig]) []int needs time.Clock | Scheduler {
	let mut woke = []int{}
	for x in trace {
		let passed = time.Clock.Now().Sub(start)
		if x.At > passed {
			Scheduler.Sleep(x.At - passed)
		}
		woke = woke.Append(int(int64(time.Clock.Now().Sub(start))))
	}
	woke
}

func timed(m MT, trace []machines.Timed[Sig], end int) string CLOCKED {
	let start = time.Clock.Now()
	let side = task.Go() { sleeper(start, trace) }
	let got = timedRun(m, trace, end)
	let ran = time.Clock.Now().Sub(start)
	let woke = side.Wait()
	"\(got) @\(int64(ran)) \(ints(woke))"
}

`, " NEEDS", helperRow), " CLOCKED", " needs time.Clock | Scheduler"+strings.ReplaceAll(helperRow, " needs ", " | ")), " MT", " "+machineType))
	// machineOf is the machine value a run of an input starts from, and
	// startOf the reference's first state for it (V4).
	machineOf := func(x, y int) (string, runState) {
		switch inputs {
		case 1:
			return fmt.Sprintf("machines.Start(im, %d)", x), runState{init.v, x + init.n}
		case 2:
			return fmt.Sprintf("machines.Start(im, (%d, %d))", x, y), runState{init.v, x - y + init.n}
		}
		return "M{}", init
	}
	m0, s0 := machineOf(r.Intn(9)-4, r.Intn(9)-4)
	fmt.Fprintf(&b, "func main() needs Console {\n")
	if inputs > 0 {
		// One InputMachine value starts every run, each from its own input.
		input := map[int]string{1: "int", 2: "(int, int)"}[inputs]
		fmt.Fprintf(&b, "\tlet im InputMachine[%s, St, Sig, int, Boom%s] = M{}\n", input, strings.ReplaceAll(helperRow, " needs ", ", "))
	}
	fmt.Fprintf(&b, "\tConsole.Println(show(machines.Initial(%s)))\n", m0)
	// Every timed run is on one VirtualClock, which a task sleeps on beside
	// it (runtime.md, Machines and actors 9.6, one clock).
	providers := "Scheduler: c, time.Clock: c"
	if needs {
		providers = "Probe: Quiet, " + providers
	}
	fmt.Fprintf(&b, "\tlet c = testing.NewVirtualClock(0)\n\twith context { %s } {\n", providers)
	want := []string{s0.text()}
	for l := range 4 {
		mv, init := machineOf(r.Intn(9)-4, r.Intn(9)-4)
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
		fmt.Fprintf(&b, "\tlet m%d = %s\n\tlet xs%d = []Sig{%s}\n\tConsole.Println(run(m%d, xs%d))\n\tConsole.Println(split(m%d, xs%d[0:%d], xs%d[%d:%d]))\n", l, mv, l, strings.Join(lit, ", "), l, l, l, l, h, l, h, n)
		fmt.Fprintf(&b, "\tConsole.Println(timed(m%d, []machines.Timed[Sig]{%s}, %d))\n", l, strings.Join(tlit, ", "), end)
		whole, _, _, _ := foldRun(table, init, signals, 0, nil)
		want = append(want, whole)
		if _, at, running, out := foldRun(table, init, signals[:h], 0, nil); running {
			rest, _, _, _ := foldRun(table, at, signals[h:], h, out)
			want = append(want, rest)
		} else {
			want = append(want, "-")
		}
		told, last := foldTimed(table, timers, init, trace, end, seen)
		// The task beside the run wakes at each time of the trace, or at
		// once when the clock is past it.
		var woke []int
		clock := 0
		for _, x := range trace {
			clock = max(clock, x.at)
			woke = append(woke, clock)
		}
		want = append(want, fmt.Sprintf("%s @%d %s", told, last, intsText(woke)))
	}
	b.WriteString("\t}\n}\n")
	return machineProgram{name: fmt.Sprintf("machinerun_%02d", k), src: b.String(), want: want}
}

// TestMachineRunAgreesWithFold is the acceptance property: over generated
// step tables, timers and signal lists, machines.Run, machines.From and
// machines.Initial print what the reference fold computes, Emit outputs
// in order; the split law (Run over a ++ b is From over b after a) holds
// on every list; and machines.RunTimed over the list at generated times
// prints what the discrete-event model foldTimed computes (V5: a timer
// never fires after its entry ended, Stay keeps it, Enter and Emit
// restart it, a signal at a timer's deadline goes first). Each timed run
// is on one testing.VirtualClock with a task beside it that sleeps until
// each time of the trace on the same clock (runtime.md, Machines and
// actors 9.6, one clock): the clock's reading when the run returns is the
// model's last instant, and the task wakes at the trace's times, so the
// machine's timers and the task's sleeps move one clock. A RunTimed that
// keeps time of its own reads 0 there. A failure prints the program.
// ERGO_MACHINERUN_SEED replays another seed.
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
	var progs []machineProgram
	var builds []*lowered
	kinds, seen := map[string]bool{}, map[string]bool{}
	// The generated runs must reach every way a run ends, an Emit's
	// output, and every timer case of the model: programs are generated
	// until they do, 16 at least and 128 at most.
	ends := []string{"running", "finished", "rejected", "boom", "-", "emitted"}
	cases := []string{"timer fired after a stay", "a stale timer came due", "signal at its timer's deadline", "timer just before a signal", "timer rejected",
		"timer fired in an entry opened by first", "timer fired in an entry opened by enter", "timer fired in an entry opened by emit", "a timer of an entry a late signal opened fired",
		"a machine with 0 inputs", "a machine with 1 inputs", "a machine with 2 inputs"}
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
			w, _, _ = strings.Cut(w, " @") // a timed run's outcome, before its times
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
		stdout, stderr, code, ended := runMachineBounded(t, exes[p.name])
		if !ended {
			t.Fatalf("seed %d, %s: still running at the test's deadline, stopped: a run that never ends (a timer cascade?)\nsource:\n%s", seed, p.name, p.src)
		}
		if got := lines(stdout); code != 0 || !slices.Equal(got, p.want) {
			t.Errorf("seed %d, %s: exit %d\nstdout:\n%s\nwant:\n%s\nstderr:\n%s\nsource:\n%s", seed, p.name, code, stdout, strings.Join(p.want, "\n"), firstLines(stderr, 8), p.src)
		}
	}
}

// runMachineBounded runs a generated program in a process group of its own and
// stops the group at the test's deadline (internal/testwait), reporting
// whether the program ended by itself. Each one ends in milliseconds; the
// bound is a guard, not a timing claim, so a loaded host never decides it. A wrong runner (a mutant whose timer never moves the clock)
// loops forever and may grow without limit: on 2026-11-08 one such
// program, orphaned when go test's timeout ended the test, took 46.7 GB.
// Its memory is bounded as every program a test starts is
// (internal/proclimit: the test kit's limit and the highest
// oom_score_adj); the process group stays, so the stop reaches the whole
// tree the program started.
func runMachineBounded(t *testing.T, exe string) (stdout, stderr string, code int, ended bool) {
	t.Helper()
	probeRan(exe)
	limit, err := proclimit.Limit(os.Environ(), proclimit.DefaultLimit)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := proclimit.Start(cmd, limit); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	stopGroup := func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	t.Cleanup(stopGroup) // a Fatal elsewhere never leaves the group behind
	select {
	case err = <-done:
		ended = true
	case <-testwait.After(t):
		stopGroup()
		<-done
		return out.String(), errOut.String(), -1, false
	}
	var limited *proclimit.LimitError
	if errors.As(proclimit.Check(exe, limit, err, errOut.Bytes()), &limited) {
		t.Fatal(limited)
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		code = exit.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return out.String(), errOut.String(), code, ended
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
	want := "cannot use M as Machine[St, Sig, string, , ] in let _: its step step gives a Machine with output int, error row {Boom} and capability row {}"
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
		"29:10: S102 this call requires Probe, which is not in the capability row of function main and no with provides it",
		"29:10: S105 the failure {Boom} of this expression is not handled: use try, catch or return (rows.md, Error rows 3)",
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
			"S108 the after timer of machine M must have the type func(St) Option[Timeout[Sig]], with no error or capability row: the runner calls it after a commit (runtime.md, Machines and actors 9)"},
		{"row", "func timer(s St) Option[Timeout[Sig]] {\n\tConsole.Println(\"x\")\n\tNone\n}\n\n",
			"S108 cannot use func(St) Option[Timeout[Sig]] needs Console as func(St) Option[Timeout[Sig]] in machine M's after: its capability row {Console} is wider than {}"},
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

// TestMachineStartRefusals: a machine's initial may name a start function
// of its input (V4): one with no parameters or another result, or with a
// row, is refused, and an InputMachine is not a Machine. Diagnostic rows:
// they alone catch a checker that admits such a start, which no program
// that builds can show.
func TestMachineStartRefusals(t *testing.T) {
	head := `type St = A{n int}
type Sig = Go
type Go {}

func step(s St, e Sig) Decision[St, int] { Ignore }

type M machine[St, Sig] {
	initial: start,
	step:    step,
}

`
	for _, c := range []struct{ name, rest, want string }{
		{"none", "func start() St { A{n: 1} }\n\nfunc main() {}\n",
			"S108 the start start of machine M must have the type func(…) St: its parameters are the machine's input, and it has no type parameters and no error or capability row (runtime.md, Machines and actors 10)"},
		{"row", "func start(n int) St {\n\tConsole.Println(\"x\")\n\tA{n: n}\n}\n\nfunc main() {}\n",
			"S108 cannot use func(int) St needs Console as func(int) St in machine M's start: its capability row {Console} is wider than {}"},
		{"machine", "func start(n int) St { A{n: n} }\n\nfunc main() {\n\tlet _ Machine[St, Sig, int] = M{}\n}\n",
			"its initial is a function of an input, so it is an InputMachine[int, …]; machines.Start(value, input) gives a Machine"},
	} {
		var got []string
		for _, d := range lowerSource("machinestart", head+c.rest, true).diags {
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
import testing
import time

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
	let c = testing.NewVirtualClock(0)
	let timed = with context { Scheduler: c, time.Clock: c } { timedTally(trace, at) }
	from(0, signals, 0, []int{}) == timed
}

func timedTally(trace []machines.Timed[int], end duration) string needs time.Clock | Scheduler {
	let o = machines.RunTimed(tally(), trace, end) catch {
	case machines.Rejected as r:
		return "rejected \(r.At)"
	}
	told(o, []int{})
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
	"machines UntimedRunsAsRun":  `UntimedRunsAsRun(signals []int, gaps []int) { let mut trace = []Timed[int]{} let mut at = 0s for i, e in signals { if i < len(gaps) { at = at + duration(int64(gaps[i] % 1000)) } trace = trace.Append(Timed[int]{At: at, Signal: e}) } let c = testing.NewVirtualClock(0) let timed = with context { Scheduler: c, time.Clock: c } { timedTally(trace, at) } from(0, signals, 0, []int{}) == timed }`,
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
