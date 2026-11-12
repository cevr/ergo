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
var MainKinds = []string{"owned", "tasks", "clock", "stop", "stdin", "args", "stderr", "exit"}

const mainPrelude = `import io
import os
import queue
import scope
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
		switch g.r.IntN(7) {
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
