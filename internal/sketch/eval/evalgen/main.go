package evalgen

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"time"
)

// MainProgram is a generated program with a main (ORACLE O4): a seeded
// sequence of scenarios over effects and tasks, each printing what it
// computed, with the arguments and standard input to run it on and the
// model's streams and exit status. Every scenario's output is fixed by the
// program, whatever schedule its tasks take: tasks report through Wait
// and a queue's one consumer, and time is a virtual clock's. Two properties
// read it: the evaluator's RunMain against the model in process, and the
// evaluator against the built program (internal/sketch/eval,
// TestEvalAgreesWithLowering).
type MainProgram struct {
	Seed   uint64
	Source string
	Args   []string
	Stdin  string
	// Stdout, Stderr and Status are the model's run.
	Stdout, Stderr string
	Status         int
	// Kinds counts the scenarios the program uses.
	Kinds map[string]int
}

// MainKinds lists every scenario a run of seeds must reach.
var MainKinds = []string{"owned", "tasks", "clock", "stop", "stdin", "args", "stderr", "exit", "range-place", "funcs", "machine", "borrows", "seq", "actor"}

const mainPrelude = `import actor
import io
import machines
import os
import queue
import scope
import strings
import sync
import task
import testing
import time

let journal = sync.NewRef[[]string]([]string{})

func note(s string) { journal.Update(func(xs []string) []string { xs.Append(s) }) }

func flush() needs Console {
	let xs = journal.Get()
	for s in xs {
		Console.Println(s)
	}
	journal.Set([]string{})
}

type Res { name string }

type Box { a []int, b []int }

func twice(x int) int { x * 2 }

func pick[T](a T, b T) T {
	let xs = []T{a, b}
	xs[len(xs) - 1]
}

func apply(f func(x int) int, x int) int { f(x) }

func shout(s string) string { strings.ToUpper(s) }

func ints(xs []int) Seq[int] {
	Seq[int](func(yield func(v int) bool) {
		for x in xs {
			if !yield(x) {
				return
			}
		}
	})
}

func firstOver(xs Seq[int], limit int) int {
	for v in xs {
		if v > limit {
			return v
		}
	}
	-1
}

type Acc { total int, items []int }

func push(a *Acc, v int) {
	a.items = a.items.Append(v)
	a.total = a.total + v
}

func bump(a *Acc, i int) { a.items[i] = a.items[i] * 10 }

func size(a Acc) int { len(a.items) }

func both(a *Acc, v int) {
	push(a, v)
	if a.size() == 3 {
		bump(a, 0)
	}
}

type Ctr { n int, limit int }

type Sig = Add{k int} | Tick{}

func ctrStart(limit int) Ctr { Ctr{n: 0, limit: limit} }

func ctrStep(c Ctr, s Sig) Decision[Ctr, int] {
	match s {
	case Add as a:
		if c.n + a.k > c.limit {
			return Reject
		}
		if a.k == 0 {
			return Ignore
		}
		Emit[Ctr, int]{Next: Ctr{n: c.n + a.k, limit: c.limit}, Output: c.n + a.k}
	case Tick:
		if c.n >= c.limit {
			return Done[int]{Output: c.n}
		}
		Stay[Ctr]{Next: Ctr{n: c.n + 1, limit: c.limit}}
	}
}

func ctrAfter(c Ctr) Option[Timeout[Sig]] {
	if c.n % 2 == 0 {
		return Some[Timeout[Sig]]{Value: Timeout[Sig]{In: time.Times(1ms, c.n + 1), Signal: Tick}}
	}
	None
}

type Counter machine[Ctr, Sig] { initial: ctrStart, step: ctrStep, after: ctrAfter }

type Tally machine[Ctr, Sig] { initial: ctrStart, step: ctrStep }

func told(d Decision[Ctr, int]) string {
	match d {
	case Enter[Ctr]:
		"enter"
	case Stay[Ctr]:
		"stay"
	case Emit[Ctr, int] as e:
		"emit \(e.Output)"
	case Ignore:
		"ignore"
	case Reject:
		"reject"
	case Done[int] as x:
		if x.Output < 0 {
			return "stopped"
		}
		"done \(x.Output)"
	}
}

func Close(r Res) { note("close " + r.name) }

func use(r Res) { note("use " + r.name) }

func take(r Res) takes r {
	use(r)
}

// imports uses every import, which a program's scenarios may not.
func imports() needs Scheduler | time.Clock {
	scope.OnExit() { note("held") }
	let q = queue.New[int](1)
	q.End()
	let t = task.Go() { 0 }
	let _ = t.Wait()
	let _ = testing.NewVirtualClock(0)
	Scheduler.Sleep(0s)
	let _ = time.Clock.Now()
	let _ = machines.Initial(machines.Start(Counter{}, 1))
	let _ = actor.StartMachine(machines.Start(Tally{}, 1), 0, func(o int) {})
}

// recorder uses io and os too.
func recorder() fails os.Exit {
	let _ = io.NewRecorder()
	return os.Exit{Code: 1, Message: ""}
}
`

type mainGen struct {
	r      *rand.Rand
	b      strings.Builder
	out    strings.Builder
	errs   strings.Builder
	kinds  map[string]int
	calls  []string
	status int
}

func (g *mainGen) line(format string, args ...any) { fmt.Fprintf(&g.b, format+"\n", args...) }

// GenerateMain generates the program of seed and its model run.
func GenerateMain(seed uint64) MainProgram {
	g := &mainGen{r: rand.New(rand.NewPCG(seed, 0x6d61696e)), kinds: map[string]int{}}
	g.b.WriteString(mainPrelude)
	p := MainProgram{Seed: seed}
	for i := range g.r.IntN(3) {
		p.Args = append(p.Args, fmt.Sprintf("a%d-%d", i, g.r.IntN(100)))
	}
	var lines []string
	for range g.r.IntN(4) {
		lines = append(lines, strings.Repeat("x", g.r.IntN(5)))
	}
	p.Stdin = strings.Join(lines, "\n")
	if len(lines) > 0 && g.r.IntN(2) == 0 {
		p.Stdin += "\n"
	}
	n := 3 + g.r.IntN(4)
	for k := range n {
		switch g.r.IntN(13) {
		case 12:
			g.actor(k)
		case 11:
			g.seq(k)
		case 10:
			g.borrows(k)
		case 9:
			g.machine(k)
		case 7:
			g.rangePlace(k)
		case 8:
			g.funcs(k)
		case 0:
			g.owned(k)
		case 1:
			g.tasks(k)
		case 2:
			g.clock(k)
		case 3:
			g.stop(k)
		case 4:
			g.stdin(k, p.Stdin)
		case 5:
			g.args(k, p.Args)
		case 6:
			g.stderr(k)
		}
	}
	g.line("")
	g.line("func main() fails os.Exit needs Console | Ambient {")
	for _, c := range g.calls {
		g.line("\t%s", c)
	}
	if g.r.IntN(3) == 0 {
		g.kinds["exit"]++
		code := 1 + g.r.IntN(5)
		g.line("\treturn os.Exit{Code: %d, Message: \"bye %d\"}", code, code)
		fmt.Fprintf(&g.errs, "bye %d\n", code)
		g.status = code
	}
	g.line("}")
	p.Source, p.Stdout, p.Stderr, p.Status, p.Kinds = g.b.String(), g.out.String(), g.errs.String(), g.status, g.kinds
	return p
}

// owned: two locals released last first, one handed to a taking callee.
func (g *mainGen) owned(k int) {
	g.kinds["owned"]++
	a, b := fmt.Sprintf("s%d.a", k), fmt.Sprintf("s%d.b", k)
	g.line("")
	g.line("func s%d() {", k)
	g.line("\tlet a = Res{name: %q}", a)
	g.line("\tlet b = Res{name: %q}", b)
	g.line("\tuse(a)")
	g.line("\ttake(b)")
	g.line("\tnote(\"s%d end\")", k)
	g.line("}")
	g.calls = append(g.calls, fmt.Sprintf("s%d()", k), "flush()")
	for _, l := range []string{"use " + a, "use " + b, "close " + b, fmt.Sprintf("s%d end", k), "close " + a} {
		g.out.WriteString(l + "\n")
	}
}

// tasks: producers sum a range and push it into a queue; one consumer
// sums what it pops.
func (g *mainGen) tasks(k int) {
	g.kinds["tasks"]++
	np := 1 + g.r.IntN(3)
	g.line("")
	g.line("func s%d() needs Console {", k)
	g.line("\tlet q = queue.New[int](%d)", g.r.IntN(3))
	g.line("\tlet cons = task.Go() {")
	g.line("\t\tlet mut t = 0")
	g.line("\t\tfor v in q.Values() {")
	g.line("\t\t\tt = t + v")
	g.line("\t\t}")
	g.line("\t\tt")
	g.line("\t}")
	total := 0
	var sums []int
	for i := range np {
		m, mul := 1+g.r.IntN(5), 1+g.r.IntN(4)
		g.line("\tlet p%d = task.Go() {", i)
		g.line("\t\tlet mut s = 0")
		g.line("\t\tfor i in 0..%d {", m)
		g.line("\t\t\ts = s + i * %d", mul)
		g.line("\t\t\tq.Push(i) catch {")
		g.line("\t\t\tcase queue.Closed:")
		g.line("\t\t\t}")
		g.line("\t\t}")
		g.line("\t\ts")
		g.line("\t}")
		s := 0
		for x := range m {
			s += x * mul
			total += x
		}
		sums = append(sums, s)
	}
	var parts []string
	for i := range np {
		g.line("\tlet r%d = p%d.Wait()", i, i)
		parts = append(parts, fmt.Sprintf("\\(r%d)", i))
	}
	g.line("\tq.End()")
	g.line("\tlet total = cons.Wait()")
	g.line("\tConsole.Println(\"s%d %s \\(total)\")", k, strings.Join(parts, " "))
	g.line("}")
	g.calls = append(g.calls, fmt.Sprintf("s%d()", k))
	fmt.Fprintf(&g.out, "s%d", k)
	for _, s := range sums {
		fmt.Fprintf(&g.out, " %d", s)
	}
	fmt.Fprintf(&g.out, " %d\n", total)
}

// clock: tasks sleep on a virtual clock and report the time they woke.
func (g *mainGen) clock(k int) {
	g.kinds["clock"]++
	nt := 1 + g.r.IntN(3)
	g.line("")
	g.line("func s%d() needs Console {", k)
	g.line("\tlet c = testing.NewVirtualClock(0)")
	g.line("\twith context { Scheduler: c, time.Clock: c } {")
	g.line("\t\tlet start = time.Clock.Now()")
	var parts []string
	var model []string
	for i := range nt {
		d1, d2 := time.Duration(1+g.r.IntN(5))*time.Millisecond, time.Duration(g.r.IntN(3))*time.Second
		g.line("\t\tlet t%d = task.Go() {", i)
		g.line("\t\t\tScheduler.Sleep(%dms)", d1/time.Millisecond)
		g.line("\t\t\tScheduler.Sleep(%ds)", d2/time.Second)
		g.line("\t\t\ttime.Clock.Now().Sub(start)")
		g.line("\t\t}")
		parts = append(parts, fmt.Sprintf("\\(t%d.Wait())", i))
		model = append(model, (d1 + d2).String())
	}
	g.line("\t\tConsole.Println(\"s%d %s \\(time.Clock.Now().Sub(start))\")", k, strings.Join(parts, " "))
	g.line("\t}")
	g.line("}")
	g.calls = append(g.calls, fmt.Sprintf("s%d()", k))
	// The root waits for each in turn: the clock reads the last wake.
	var latest time.Duration
	for _, m := range model {
		d, _ := time.ParseDuration(m)
		latest = max(latest, d)
	}
	fmt.Fprintf(&g.out, "s%d %s %s\n", k, strings.Join(model, " "), latest)
}

// stop: a task that would sleep an hour is stopped once it sleeps: its
// exit action runs and nothing after its sleep does.
func (g *mainGen) stop(k int) {
	g.kinds["stop"]++
	g.line("")
	g.line("func s%d() needs Console {", k)
	g.line("\tlet c = testing.NewVirtualClock(0)")
	g.line("\twith context { Scheduler: c, time.Clock: c } {")
	g.line("\t\tlet z = task.Go() {")
	g.line("\t\t\tscope.OnExit() { note(\"s%d z exit\") }", k)
	g.line("\t\t\tScheduler.Sleep(1h)")
	g.line("\t\t\tnote(\"s%d z woke\")", k)
	g.line("\t\t}")
	g.line("\t\tScheduler.Sleep(1ms)")
	g.line("\t\tz.Stop()")
	g.line("\t}")
	g.line("\tflush()")
	g.line("}")
	g.calls = append(g.calls, fmt.Sprintf("s%d()", k))
	fmt.Fprintf(&g.out, "s%d z exit\n", k)
}

// stdin: the lines of standard input, counted, once: a later read sees
// nothing.
func (g *mainGen) stdin(k int, in string) {
	g.kinds["stdin"]++
	g.line("")
	g.line("func s%d() needs Console | Ambient {", k)
	g.line("\twith context { io.Stdin: io.NewStdin() } {")
	g.line("\t\tlet mut n = 0")
	g.line("\t\tlet mut chars = 0")
	g.line("\t\tfor line in io.Stdin.Lines() {")
	g.line("\t\t\tn = n + 1")
	g.line("\t\t\tchars = chars + len(line)")
	g.line("\t\t}")
	g.line("\t\tConsole.Println(\"s%d lines \\(n) \\(chars)\")", k)
	g.line("\t}")
	g.line("}")
	g.calls = append(g.calls, fmt.Sprintf("s%d()", k))
	n, chars := 0, 0
	if g.kinds["stdin"] == 1 && in != "" {
		lines := strings.Split(strings.TrimSuffix(in, "\n"), "\n")
		n = len(lines)
		for _, l := range lines {
			chars += len(l)
		}
	}
	fmt.Fprintf(&g.out, "s%d lines %d %d\n", k, n, chars)
}

// args: the process's arguments.
func (g *mainGen) args(k int, args []string) {
	g.kinds["args"]++
	g.line("")
	g.line("func s%d() needs Console | Ambient {", k)
	g.line("\twith context { os.Process: os.NewHost() } {")
	g.line("\t\tlet args = os.Process.Args()")
	g.line("\t\tConsole.Println(\"s%d args \\(len(args))\")", k)
	g.line("\t\tfor a in args {")
	g.line("\t\t\tConsole.Println(a)")
	g.line("\t\t}")
	g.line("\t}")
	g.line("}")
	g.calls = append(g.calls, fmt.Sprintf("s%d()", k))
	fmt.Fprintf(&g.out, "s%d args %d\n", k, len(args))
	for _, a := range args {
		g.out.WriteString(a + "\n")
	}
}

// rangePlace: a for over a slice reached through a subscript and a field
// sees the writes through that place to its later elements, and not the
// writes through another place that held the same slice when the loop
// began (a field it was copied to, the sibling row it was copied from).
func (g *mainGen) rangePlace(k int) {
	g.kinds["range-place"]++
	n := 2 + g.r.IntN(3)
	vs := make([]int, n)
	lits := make([]string, n)
	for i := range vs {
		vs[i] = g.r.IntN(10)
		lits[i] = fmt.Sprint(vs[i])
	}
	row, j := g.r.IntN(2), g.r.IntN(n)
	g.line("")
	g.line("func s%d() needs Console {", k)
	g.line("\tlet mut bx = Box{a: []int{%s}, b: []int{}}", strings.Join(lits, ", "))
	g.line("\tbx.b = bx.a")
	g.line("\tlet mut rows = []Box{bx, bx}")
	g.line("\tlet mut s = 0")
	g.line("\tfor x in rows[%d].a {", row)
	g.line("\t\ts = s * 7 + x")
	// The other places first: while they still share the loop's store.
	if g.r.IntN(2) == 0 {
		g.line("\t\trows[%d].b[%d] = 100", row, j)
		g.line("\t\trows[%d].a[%d] = 1000", 1-row, j)
	} else {
		g.line("\t\trows[%d].a[%d] = 1000", 1-row, j)
		g.line("\t\trows[%d].b[%d] = 100", row, j)
	}
	g.line("\t\trows[%d].a[%d] = rows[%d].a[%d] + 10", row, j, row, j)
	g.line("\t}")
	g.line("\tConsole.Println(\"s%d \\(s) \\(rows[%d].a[%d]) \\(rows[%d].b[%d]) \\(rows[%d].a[%d])\")", k, row, j, row, j, 1-row, j)
	g.line("}")
	g.calls = append(g.calls, fmt.Sprintf("s%d()", k))
	s := 0
	for i, v := range vs {
		if i == j {
			v += 10 * i // the writes of the turns before this one
		}
		s = s*7 + v
	}
	fmt.Fprintf(&g.out, "s%d %d %d 100 1000\n", k, s, vs[j]+10*n)
}

// funcs: declared functions as values: a package function, a generic
// one's instance and a std function the evaluator runs natively, each
// held in a local and called through it or through a parameter.
func (g *mainGen) funcs(k int) {
	g.kinds["funcs"]++
	x, m := g.r.IntN(50), 2+g.r.IntN(3)
	a, b := fmt.Sprintf("p%d", g.r.IntN(9)), fmt.Sprintf("q%d", g.r.IntN(9))
	g.line("")
	g.line("func s%d() needs Console {", k)
	g.line("\tlet f = twice")
	g.line("\tlet g = pick[string]")
	g.line("\tlet h = strings.ToUpper")
	g.line("\tlet mut n = %d", x)
	g.line("\tfor i in 0..%d {", m)
	g.line("\t\tn = apply(f, n) + i")
	g.line("\t}")
	g.line("\tConsole.Println(\"s%d \\(n) \\(g(%q, %q)) \\(h(%q))\")", k, a, b, a)
	g.line("}")
	g.calls = append(g.calls, fmt.Sprintf("s%d()", k))
	n := x
	for i := range m {
		n = n*2 + i
	}
	fmt.Fprintf(&g.out, "s%d %d %s %s\n", k, n, b, strings.ToUpper(a))
}

// machine: a machine declaration's value, converted to the prelude's
// Machine through machines.Start (its initial is a function of an input),
// folded by machines.Run over a seeded list of signals; its After read for
// the state the run ended in, and its Work, which it does not declare.
func (g *mainGen) machine(k int) {
	g.kinds["machine"]++
	limit := 3 + g.r.IntN(8)
	var sigs []string
	type sig struct{ add, k int }
	var model []sig
	for range 1 + g.r.IntN(6) {
		if g.r.IntN(3) == 0 {
			sigs, model = append(sigs, "Tick"), append(model, sig{})
			continue
		}
		v := g.r.IntN(4)
		sigs, model = append(sigs, fmt.Sprintf("Add{k: %d}", v)), append(model, sig{add: 1, k: v})
	}
	g.line("")
	g.line("func s%d() needs Console {", k)
	g.line("\tlet m = machines.Start(Counter{}, %d)", limit)
	g.line("\tlet o = machines.Run(m, []Sig{%s}) catch {", strings.Join(sigs, ", "))
	g.line("\tcase machines.Rejected as r:")
	g.line("\t\tConsole.Println(\"s%d rejected \\(r.At)\")", k)
	g.line("\t\treturn")
	g.line("\t}")
	g.line("\tlet mut last = m.Initial")
	g.line("\tmatch o {")
	g.line("\tcase machines.Finished[Ctr, int] as f:")
	g.line("\t\tConsole.Println(\"s%d done \\(f.State.n) \\(f.Output) \\(len(f.Emitted))\")", k)
	g.line("\t\tlast = f.State")
	g.line("\tcase machines.Running[Ctr, int] as r:")
	g.line("\t\tConsole.Println(\"s%d running \\(r.State.n) \\(len(r.Emitted))\")", k)
	g.line("\t\tlast = r.State")
	g.line("\t}")
	g.line("\tmatch m.After(last) {")
	g.line("\tcase Some[Timeout[Sig]] as t:")
	g.line("\t\tConsole.Println(\"s%d timer \\(t.Value.In)\")", k)
	g.line("\tcase None:")
	g.line("\t\tConsole.Println(\"s%d no timer\")", k)
	g.line("\t}")
	g.line("\tmatch m.Work(last) {")
	g.line("\tcase Some:")
	g.line("\t\tConsole.Println(\"s%d work\")", k)
	g.line("\tcase None:")
	g.line("\t\tConsole.Println(\"s%d no work\")", k)
	g.line("\t}")
	g.line("}")
	g.calls = append(g.calls, fmt.Sprintf("s%d()", k))
	n, emitted := 0, 0
	for i, s := range model {
		switch {
		case s.add == 0 && n >= limit:
			fmt.Fprintf(&g.out, "s%d done %d %d %d\n", k, n, n, emitted)
			g.machineTail(k, n)
			return
		case s.add == 0:
			n++
		case n+s.k > limit:
			fmt.Fprintf(&g.out, "s%d rejected %d\n", k, i)
			return
		case s.k == 0:
		default:
			n += s.k
			emitted++
		}
	}
	fmt.Fprintf(&g.out, "s%d running %d %d\n", k, n, emitted)
	g.machineTail(k, n)
}

// machineTail is the model of the timer and work lines of machine.
func (g *mainGen) machineTail(k, n int) {
	if n%2 == 0 {
		fmt.Fprintf(&g.out, "s%d timer %s\n", k, time.Duration(n+1)*time.Millisecond)
	} else {
		fmt.Fprintf(&g.out, "s%d no timer\n", k)
	}
	fmt.Fprintf(&g.out, "s%d no work\n", k)
}

// actor: the Tally machine run as an actor (std actor) with a seeded
// inbox capacity: one Send, then an Ask of each seeded signal (an Ask
// after Done fails Stopped), the sink printing each output before the
// answer; then State, Stop and ExitOf.
func (g *mainGen) actor(k int) {
	g.kinds["actor"]++
	limit, capacity := 3+g.r.IntN(6), g.r.IntN(3)
	type sig struct {
		tick bool
		k    int
		lit  string
	}
	pick := func() sig {
		if g.r.IntN(3) == 0 {
			return sig{tick: true, lit: "Tick"}
		}
		v := g.r.IntN(4)
		return sig{k: v, lit: fmt.Sprintf("Add{k: %d}", v)}
	}
	first := pick()
	asks := make([]sig, 1+g.r.IntN(6))
	for i := range asks {
		asks[i] = pick()
	}
	g.line("")
	g.line("func s%d() needs Console | Ambient {", k)
	g.line("\twith context { Scheduler: NewLiveScheduler() } {")
	g.line("\t\tactor%d()", k)
	g.line("\t}")
	g.line("}")
	g.line("")
	g.line("func actor%d() needs Console | Scheduler {", k)
	g.line("\tlet a = actor.StartMachine(machines.Start(Tally{}, %d), %d, func(o int) { note(\"s%d out \\(o)\") })", limit, capacity, k)
	g.line("\ta.Send(%s) catch {", first.lit)
	g.line("\tcase actor.Stopped:")
	g.line("\t\tConsole.Println(\"s%d send stopped\")", k)
	g.line("\t}")
	for i, s := range asks {
		g.line("\tlet d%d = a.Ask(%s) catch {", i, s.lit)
		g.line("\tcase actor.Stopped:")
		g.line("\t\tDone[int]{Output: -1}")
		g.line("\t}")
		g.line("\tflush()")
		g.line("\tConsole.Println(\"s%d \\(told(d%d))\")", k, i)
	}
	g.line("\tConsole.Println(\"s%d state \\(a.State().n)\")", k)
	g.line("\ta.Stop()")
	g.line("\tmatch actor.ExitOf(a) {")
	g.line("\tcase actor.Finished[Ctr, int] as f:")
	g.line("\t\tConsole.Println(\"s%d finished \\(f.State.n) \\(f.Output)\")", k)
	g.line("\tcase actor.Halted[Ctr] as h:")
	g.line("\t\tConsole.Println(\"s%d halted \\(h.State.n)\")", k)
	g.line("\tcase _:")
	g.line("\t\tConsole.Println(\"s%d other\")", k)
	g.line("\t}")
	g.line("}")
	g.calls = append(g.calls, fmt.Sprintf("s%d()", k))
	n, ended := 0, false
	// apply steps s; quiet drops the decision's line (a Send's).
	apply := func(s sig, quiet bool) {
		say := func(f string, args ...any) {
			if !quiet {
				fmt.Fprintf(&g.out, "s%d "+f+"\n", append([]any{k}, args...)...)
			}
		}
		switch {
		case ended:
			say("stopped")
		case s.tick && n >= limit:
			fmt.Fprintf(&g.out, "s%d out %d\n", k, n)
			say("done %d", n)
			ended = true
		case s.tick:
			n++
			say("stay")
		case n+s.k > limit:
			say("reject")
		case s.k == 0:
			say("ignore")
		default:
			n += s.k
			fmt.Fprintf(&g.out, "s%d out %d\n", k, n)
			say("emit %d", n)
		}
	}
	apply(first, true)
	for _, s := range asks {
		apply(s, false)
	}
	fmt.Fprintf(&g.out, "s%d state %d\n", k, n)
	if ended {
		fmt.Fprintf(&g.out, "s%d finished %d %d\n", k, n, n)
	} else {
		fmt.Fprintf(&g.out, "s%d halted %d\n", k, n)
	}
}

// seq: a for over a Seq that a function literal makes, Seq[int](f), with
// a continue and a break in its body, and a return from a for over one.
func (g *mainGen) seq(k int) {
	g.kinds["seq"]++
	limit := 2 + g.r.IntN(6)
	var xs []int
	var lits []string
	for range g.r.IntN(7) {
		v := g.r.IntN(10)
		xs = append(xs, v)
		lits = append(lits, fmt.Sprint(v))
	}
	list := "[]int{" + strings.Join(lits, ", ") + "}"
	g.line("")
	g.line("func s%d() needs Console {", k)
	g.line("\tlet mut sum = 0")
	g.line("\tlet mut n = 0")
	g.line("\tfor v in ints(%s) {", list)
	g.line("\t\tif v == 0 {")
	g.line("\t\t\tcontinue")
	g.line("\t\t}")
	g.line("\t\tif v > %d {", limit)
	g.line("\t\t\tbreak")
	g.line("\t\t}")
	g.line("\t\tsum = sum + v")
	g.line("\t\tn = n + 1")
	g.line("\t}")
	g.line("\tConsole.Println(\"s%d \\(sum) \\(n) \\(firstOver(ints(%s), %d))\")", k, list, limit)
	g.line("}")
	g.calls = append(g.calls, fmt.Sprintf("s%d()", k))
	sum, n, first := 0, 0, -1
	for _, v := range xs {
		if v == 0 {
			continue
		}
		if v > limit {
			first = v
			break
		}
		sum += v
		n++
	}
	fmt.Fprintf(&g.out, "s%d %d %d %d\n", k, sum, n, first)
}

// borrows: writes through a borrow (types.md, Borrows) reach the place it
// lends, by an explicit &x, a dot call's implicit borrow, a borrow passed
// on and a borrow of a slice's element; a copy made before is unchanged.
func (g *mainGen) borrows(k int) {
	g.kinds["borrows"]++
	v1, v2, v3, v4 := 1+g.r.IntN(9), g.r.IntN(9), g.r.IntN(9), g.r.IntN(9)
	g.line("")
	g.line("func s%d() needs Console {", k)
	g.line("\tlet mut acc = Acc{total: 0, items: []int{}}")
	g.line("\tlet before = acc")
	g.line("\tacc.push(%d)", v1)
	g.line("\tpush(&acc, %d)", v2)
	g.line("\tboth(&acc, %d)", v3)
	g.line("\tlet mut rows = []Acc{acc, acc}")
	g.line("\tpush(&rows[1], %d)", v4)
	g.line("\tConsole.Println(\"s%d \\(acc.total) \\(len(acc.items)) \\(acc.items[0]) \\(before.total) \\(rows[0].total) \\(rows[1].total) \\(acc.size())\")", k)
	g.line("}")
	g.calls = append(g.calls, fmt.Sprintf("s%d()", k))
	t := v1 + v2 + v3
	fmt.Fprintf(&g.out, "s%d %d 3 %d 0 %d %d 3\n", k, t, v1*10, t, t+v4)
}

// stderr: a line on standard error.
func (g *mainGen) stderr(k int) {
	g.kinds["stderr"]++
	g.line("")
	g.line("func s%d() needs Ambient {", k)
	g.line("\twith context { io.Stderr: io.NewErrors() } {")
	g.line("\t\tio.Stderr.Eprintln(\"s%d warns\")", k)
	g.line("\t}")
	g.line("}")
	g.calls = append(g.calls, fmt.Sprintf("s%d()", k))
	fmt.Fprintf(&g.errs, "s%d warns\n", k)
}
