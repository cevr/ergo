package evalgen

import (
	"fmt"
	mbits "math/bits"
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
var MainKinds = []string{"owned", "tasks", "clock", "stop", "stdin", "args", "stderr", "exit", "range-place", "funcs", "machine", "borrows", "seq", "actor", "kids", "bits", "maps", "deadline", "codec", "durable"}

const mainPrelude = `import actor
import bits
import io
import json
import machines
import os
import queue
import scope
import sha256
import slices
import strings
import sync
import task
import testing
import timeout
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

type Pt { X int8, Label string }
type Circle { R float64 }
type Square { Side int }
type Shape = Circle | Square
type Doc {
	Name string,
	Pts []Pt,
	Shape Shape,
	Tags Map[string, uint8],
	Pair (int, bool),
	Note json.Optional[string],
	Level json.Nullable[int],
	ID int "doc_id",
	HTTPCode int,
}

func showDoc(tag string, text string) needs Console {
	let c = json.Codec[Doc]{MaxBytes: 0, MaxDepth: 0}
	let d = json.Decode(c, text) catch {
	case json.Malformed as m:
		Console.Println(tag + " malformed \(m.Offset) \(m.Reason)")
		return
	case WireWrongKind as w:
		Console.Println(tag + " wrong \(w.Path) \(w.Want)")
		return
	case WireMissingField as w:
		Console.Println(tag + " missing \(w.Path) \(w.Field)")
		return
	case WireUnknownTag as w:
		Console.Println(tag + " tag \(w.Path) \(w.Tag)")
		return
	}
	Console.Println(tag + " " + json.Encode(c, d))
}

func showValue(tag string, text string) needs Console {
	let c = json.Codec[json.Value]{MaxBytes: 0, MaxDepth: 0}
	let v = json.Decode(c, text) catch {
	case json.Malformed as m:
		Console.Println(tag + " malformed \(m.Offset) \(m.Reason)")
		return
	case _:
		Console.Println(tag + " shape")
		return
	}
	Console.Println(tag + " " + json.Encode(c, v))
}

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

// Id, Tab and FK are the keyed half of the maps scenario: a record key, as
// dense's ID[K] is, written through borrows, and a float field whose -0
// is the key 0.
type Id { n uint32 }

type FK { x float64 }

type Tab { rows []int }

func tput(t *Tab, v int) Id {
	let id = Id{n: uint32(len(t.rows))}
	t.rows = t.rows.Append(v)
	id
}

func tset(t *Tab, id Id, v int) { t.rows[int(id.n)] = v }

func count(c *Map[Id, int], id Id, by int) {
	match (*c).Get(id) {
	case Some[int] as s:
		(*c)[id] = s.Value + by
	case None:
		(*c)[id] = by
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

type Kid { n int }

type Hop {}

func kidStart(n int) Kid { Kid{n: n} }

func kidStep(k Kid, h Hop) Decision[Kid, int] {
	if k.n <= 1 {
		return Done[int]{Output: 1}
	}
	Emit[Kid, int]{Next: Kid{n: k.n - 1}, Output: k.n}
}

type Kids machine[Kid, Hop] { initial: kidStart, step: kidStep }

type KidActor = actor.Actor[Kid, Hop, Decision[Kid, int]]

type Nest = Idle{rounds int, last int} | Busy{rounds int, n int, sum int, kid actor.Host[KidActor]}

type NestSig = Spawn{n int} | Got{v int}

func nestStart(r int) Nest { Idle{rounds: r, last: 0} }

func nestStep(s Nest, e NestSig) Decision[Nest, int] {
	match s {
	case Idle as i:
		match e {
		case Spawn as sp:
			Enter[Nest]{Next: Busy{rounds: i.rounds, n: sp.n, sum: 0, kid: actor.NewHost[KidActor]()}}
		case Got:
			Ignore
		}
	case Busy as b:
		match e {
		case Spawn:
			Reject
		case Got as g:
			if g.v == 1 {
				return Emit[Nest, int]{Next: Idle{rounds: b.rounds + 1, last: b.sum + 1}, Output: b.sum + 1}
			}
			Stay[Nest]{Next: Busy{rounds: b.rounds, n: b.n, sum: b.sum + g.v, kid: b.kid}}
		}
	}
}

func hop(c KidActor, k int) {
	if k <= 0 {
		return
	}
	c.Send(Hop) catch {
	case actor.Stopped:
		return
	}
	hop(c, k - 1)
}

func nestWork(s Nest) Option[machines.Work[NestSig, Scheduler]] {
	match s {
	case Busy as b:
		Some{machines.Work{Listen: func(send func(e NestSig)) needs Scheduler {
			let c = actor.ChildIn(send, b.kid, machines.Start(Kids{}, b.n), 2, func(o int) NestSig { Got{v: o} })
			hop(c, b.n + 1)
		}}}
	case Idle:
		None
	}
}

type Nester machine[Nest, NestSig] { initial: nestStart, step: nestStep, work: nestWork }

type Lost {}

// replayNest is the Nester's state after sigs from its start: a durable
// recover's fold, whose Spawn makes a host no entry owns yet.
func replayNest(sigs []NestSig) Nest {
	let mut s = nestStart(0)
	for e in sigs {
		match nestStep(s, e) {
		case Enter[Nest] as en:
			s = en.Next
		case Stay[Nest] as st:
			s = st.Next
		case Emit[Nest, int] as em:
			s = em.Next
		case Ignore:
		case Reject:
		case Done[int]:
		}
	}
	s
}

func idleAt(s Nest, r int) bool {
	match s {
	case Idle as i:
		i.rounds == r
	case Busy:
		false
	}
}

func lastOf(s Nest) int {
	match s {
	case Idle as i:
		i.last
	case Busy:
		-1
	}
}

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
	let _ = bits.Len(1)
	let _ = sha256.Sum("")
	let _ = slices.Repeat([]int{}, 1)
	let _ = timeout.After(1s) { 1 } catch {
	case timeout.Expired:
		0
	}
	let _ = json.Encode(json.Codec[int]{MaxBytes: 0, MaxDepth: 0}, 1)
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
		switch g.r.IntN(19) {
		case 18:
			g.durable(k)
		case 17:
			g.codec(k)
		case 16:
			g.deadline(k)
		case 15:
			g.maps(k)
		case 14:
			g.bits(k)
		case 13:
			g.kids(k)
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

// deadline: timeout.After on the live Scheduler, a body that ends at once
// (its value, or its typed failure) against a deadline of an hour, or a
// body that sleeps an hour against a deadline of a millisecond (Expired,
// the body stopped: its note never written). No two of them race.
func (g *mainGen) deadline(k int) {
	g.kinds["deadline"]++
	g.line("")
	g.line("type Late%d { n int }", k)
	g.line("")
	g.line("func late%d(n int) int fails Late%d {", k, k)
	g.line("\treturn Late%d{n: n}", k)
	g.line("}")
	g.line("")
	g.line("func s%d() needs Console | Ambient {", k)
	g.line("\twith context { Scheduler: NewLiveScheduler() } {")
	g.line("\t\tdeadline%d()", k)
	g.line("\t}")
	g.line("}")
	g.line("")
	// An Ergo function's trailing block is its last argument.
	g.line("func both%d(base int, body func() int) int { base + body() + body() }", k)
	g.line("")
	g.line("func deadline%d() needs Console | Scheduler {", k)
	tw := g.r.IntN(9)
	g.line("\tlet b%d = %d", k, tw)
	g.line("\tConsole.Println(\"s%d both \\(both%d(1) { b%d * 10 })\")", k, k, k)
	fmt.Fprintf(&g.out, "s%d both %d\n", k, 1+20*tw)
	for i := range 1 + g.r.IntN(3) {
		v := g.r.IntN(50)
		switch g.r.IntN(3) {
		case 0:
			g.line("\tlet v%d = timeout.After(1h) { %d } catch {", i, v)
			fmt.Fprintf(&g.out, "s%d %d %d\n", k, i, v)
		case 1:
			g.line("\tlet v%d = timeout.After(1h) { try late%d(%d) } catch {", i, k, v)
			g.line("\tcase Late%d as l:", k)
			g.line("\t\t-l.n")
			fmt.Fprintf(&g.out, "s%d %d %d\n", k, i, -v)
		default:
			g.line("\tlet v%d = timeout.After(1ms) {", i)
			g.line("\t\tScheduler.Sleep(1h)")
			g.line("\t\tnote(\"s%d late\")", k)
			g.line("\t\t%d", v)
			g.line("\t} catch {")
			fmt.Fprintf(&g.out, "s%d %d -1\n", k, i)
		}
		g.line("\tcase timeout.Expired:")
		g.line("\t\t-1")
		g.line("\t}")
		g.line("\tflush()")
		g.line("\tConsole.Println(\"s%d %d \\(v%d)\")", k, i, i)
	}
	g.line("}")
	g.calls = append(g.calls, fmt.Sprintf("s%d()", k))
}

// maps: fors over a seeded Map whose outcome no order decides: a count
// and a sum; a loop that deletes every other key at its first entry
// (Go visits no deleted entry, so one entry is visited in any order); a
// loop that zeroes every other value at its first entry (each entry's
// value is read when it is reached, so one value is not zero).
func (g *mainGen) maps(k int) {
	g.kinds["maps"]++
	m := map[int]int{}
	var lits []string
	for range g.r.IntN(6) {
		key, v := g.r.IntN(20)-5, 1+g.r.IntN(9)
		if _, dup := m[key]; dup {
			continue
		}
		m[key] = v
		lits = append(lits, fmt.Sprintf("%d: %d", key, v))
	}
	g.line("")
	g.line("func s%d() needs Console {", k)
	g.line("\tlet m = Map[int, int]{%s}", strings.Join(lits, ", "))
	g.line("\tlet mut n = 0")
	g.line("\tlet mut sum = 0")
	g.line("\tfor key, v in m {")
	g.line("\t\tn = n + 1")
	g.line("\t\tsum = sum + key * v")
	g.line("\t}")
	g.line("\tlet mut d = m")
	g.line("\tlet mut seen = 0")
	g.line("\tfor key, _ in d {")
	g.line("\t\tseen = seen + 1")
	g.line("\t\tfor o, _ in m {")
	g.line("\t\t\tif o != key {")
	g.line("\t\t\t\td = d.Delete(o)")
	g.line("\t\t\t}")
	g.line("\t\t}")
	g.line("\t}")
	g.line("\tlet mut z = m")
	g.line("\tlet mut live = 0")
	g.line("\tfor key, v in z {")
	g.line("\t\tif v != 0 {")
	g.line("\t\t\tlive = live + 1")
	g.line("\t\t}")
	g.line("\t\tfor o, _ in m {")
	g.line("\t\t\tif o != key {")
	g.line("\t\t\t\tz[o] = 0")
	g.line("\t\t\t}")
	g.line("\t\t}")
	g.line("\t}")
	// Record keys: each entry's row id counted twice, once by the id
	// tput returned and once by an equal record built anew, so the two
	// are one key; the -0 of a float field is the key 0.
	g.line("\tlet mut t = Tab{rows: []int{}}")
	g.line("\tlet mut c = Map[Id, int]{}")
	g.line("\tfor key, v in m {")
	g.line("\t\tlet id = t.tput(v)")
	g.line("\t\tcount(&c, id, key)")
	g.line("\t\tcount(&c, Id{n: id.n}, 1)")
	g.line("\t\tt.tset(id, v * 2)")
	g.line("\t}")
	g.line("\tlet mut total = 0")
	g.line("\tlet mut ids = 0")
	g.line("\tfor id, by in c {")
	g.line("\t\ttotal = total + by")
	g.line("\t\tids = ids + int(id.n)")
	g.line("\t}")
	g.line("\tlet mut rows = 0")
	g.line("\tfor r in t.rows {")
	g.line("\t\trows = rows + r")
	g.line("\t}")
	g.line("\tlet zero = 0.0")
	g.line("\tlet mut f = Map[FK, int]{FK{x: zero}: 1}")
	g.line("\tf[FK{x: -zero}] = 2")
	g.line("\tlet mut fv = 0")
	g.line("\tmatch f.Get(FK{x: zero}) {")
	g.line("\tcase Some[int] as s:")
	g.line("\t\tfv = s.Value")
	g.line("\tcase None:")
	g.line("\t\tfv = -1")
	g.line("\t}")
	g.line("\tf = f.Delete(FK{x: -zero})")
	g.line("\tConsole.Println(\"s%d \\(n) \\(sum) \\(seen) \\(len(d)) \\(live) \\(len(c)) \\(total) \\(ids) \\(rows) \\(fv) \\(len(f))\")", k)
	g.line("}")
	g.calls = append(g.calls, fmt.Sprintf("s%d()", k))
	sum, total, rows := 0, 0, 0
	for key, v := range m {
		sum += key * v
		total += key + 1
		rows += 2 * v
	}
	one := min(len(m), 1)
	fmt.Fprintf(&g.out, "s%d %d %d %d %d %d %d %d %d %d 2 0\n", k, len(m), sum, one, one, one, len(m), total, len(m)*(len(m)-1)/2, rows)
}

// bits: std bits of a seeded uint, slices.Repeat of a seeded list.
func (g *mainGen) bits(k int) {
	g.kinds["bits"]++
	x := g.r.Uint64() >> g.r.IntN(64)
	n := g.r.IntN(4)
	var lits []string
	var xs []int
	for range g.r.IntN(4) {
		v := g.r.IntN(10)
		xs = append(xs, v)
		lits = append(lits, fmt.Sprint(v))
	}
	g.line("")
	g.line("func s%d() needs Console {", k)
	g.line("\tlet x = uint64(%d)", x)
	g.line("\tlet ys = slices.Repeat([]int{%s}, %d)", strings.Join(lits, ", "), n)
	g.line("\tConsole.Println(\"s%d \\(bits.OnesCount64(x)) \\(bits.OnesCount(uint(x))) \\(bits.Len64(x)) \\(bits.LeadingZeros64(x)) \\(bits.TrailingZeros64(x)) \\(len(ys)) \\(len(sha256.Sum(\"s%d\")))\")", k, k)
	g.line("\tfor y in ys {")
	g.line("\t\tConsole.Println(\"s%d y \\(y)\")", k)
	g.line("\t}")
	g.line("}")
	g.calls = append(g.calls, fmt.Sprintf("s%d()", k))
	fmt.Fprintf(&g.out, "s%d %d %d %d %d %d %d 32\n", k, mbits.OnesCount64(x), mbits.OnesCount64(x), mbits.Len64(x), mbits.LeadingZeros64(x), mbits.TrailingZeros64(x), len(xs)*n)
	for range n {
		for _, y := range xs {
			fmt.Fprintf(&g.out, "s%d y %d\n", k, y)
		}
	}
}

// durable: the Nester as a durable actor over a journal (a Ref) that
// holds one Spawn: the recover replays it, so the first entry is Busy
// with the host the replay made, which that entry binds and its work
// fills with a Kids child; save journals every committed signal, so the
// journal's length at the end counts the commits.
func (g *mainGen) durable(k int) {
	g.kinds["durable"]++
	capacity := g.r.IntN(3)
	n0 := 1 + g.r.IntN(4)
	g.line("")
	g.line("func s%d() needs Console | Ambient {", k)
	g.line("\twith context { Scheduler: NewLiveScheduler() } {")
	g.line("\t\tdurable%d()", k)
	g.line("\t}")
	g.line("}")
	g.line("")
	g.line("func durable%d() needs Console | Scheduler {", k)
	g.line("\tlet j = sync.NewRef[[]NestSig]([]NestSig{Spawn{n: %d}})", n0)
	g.line("\tlet a = actor.StartDurable(machines.Start(Nester{}, 0), %d, func(o int) { note(\"s%d out \\(o)\") }, func() Option[Nest] fails Lost {", capacity, k)
	g.line("\t\tlet sigs = j.Get()")
	g.line("\t\tif len(sigs) == 0 {")
	g.line("\t\t\treturn Lost")
	g.line("\t\t}")
	g.line("\t\tSome[Nest]{Value: replayNest(sigs)}")
	g.line("\t}, func(prev Nest, next Nest, sig NestSig) fails Lost {")
	g.line("\t\tif len(j.Get()) > 1000 {")
	g.line("\t\t\treturn Lost")
	g.line("\t\t}")
	g.line("\t\tj.Update(func(xs []NestSig) []NestSig { xs.Append(sig) })")
	g.line("\t}) catch {")
	g.line("\tcase Lost:")
	g.line("\t\treturn")
	g.line("\t}")
	commits := 1
	rounds := 1 + g.r.IntN(3)
	for r := 1; r <= rounds; r++ {
		n := n0
		if r > 1 {
			n = 1 + g.r.IntN(4)
			commits++ // the Spawn's Enter
			g.line("\ta.Send(Spawn{n: %d}) catch {", n)
			g.line("\tcase actor.Stopped:")
			g.line("\t\treturn")
			g.line("\t}")
		}
		commits += n // n-1 Stays and the Emit
		g.line("\tlet w%d = actor.WaitFor(a, func(s Nest) bool { idleAt(s, %d) }) catch {", r, r)
		g.line("\tcase actor.Stopped:")
		g.line("\t\treturn")
		g.line("\t}")
		g.line("\tflush()")
		g.line("\tConsole.Println(\"s%d round \\(lastOf(w%d))\")", k, r)
		sum := n * (n + 1) / 2
		fmt.Fprintf(&g.out, "s%d out %d\ns%d round %d\n", k, sum, k, sum)
	}
	g.line("\ta.Stop()")
	g.line("\tConsole.Println(\"s%d journal \\(len(j.Get()))\")", k)
	fmt.Fprintf(&g.out, "s%d journal %d\n", k, commits)
	g.line("}")
	g.calls = append(g.calls, fmt.Sprintf("s%d()", k))
}

// kids: the Nester actor, each Spawn entering a Busy entry whose host
// its work fills with a Kids child (actor.ChildIn); the child counts down
// on the work's Hops, its outputs reach the parent as Got signals, and
// its Done ends the round with an Emit of the sum.
func (g *mainGen) kids(k int) {
	g.kinds["kids"]++
	capacity := g.r.IntN(3)
	g.line("")
	g.line("func s%d() needs Console | Ambient {", k)
	g.line("\twith context { Scheduler: NewLiveScheduler() } {")
	g.line("\t\tkids%d()", k)
	g.line("\t}")
	g.line("}")
	g.line("")
	g.line("func kids%d() needs Console | Scheduler {", k)
	g.line("\tlet a = actor.StartMachine(machines.Start(Nester{}, 0), %d, func(o int) { note(\"s%d out \\(o)\") })", capacity, k)
	rounds := 1 + g.r.IntN(3)
	for r := 1; r <= rounds; r++ {
		n := 1 + g.r.IntN(4)
		g.line("\ta.Send(Spawn{n: %d}) catch {", n)
		g.line("\tcase actor.Stopped:")
		g.line("\t\treturn")
		g.line("\t}")
		g.line("\tlet w%d = actor.WaitFor(a, func(s Nest) bool { idleAt(s, %d) }) catch {", r, r)
		g.line("\tcase actor.Stopped:")
		g.line("\t\treturn")
		g.line("\t}")
		g.line("\tflush()")
		g.line("\tConsole.Println(\"s%d round \\(lastOf(w%d))\")", k, r)
		sum := n * (n + 1) / 2
		fmt.Fprintf(&g.out, "s%d out %d\ns%d round %d\n", k, sum, k, sum)
	}
	g.line("\ta.Stop()")
	g.line("}")
	g.calls = append(g.calls, fmt.Sprintf("s%d()", k))
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
