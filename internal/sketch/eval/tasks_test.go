package eval

import (
	"fmt"
	"math/rand/v2"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// TestEvalTasksAgreeWithModel is the task property of ORACLE O3 (AGENTS.md,
// Tests: a seeded property against an independent reference model;
// runtime.md, Tasks, Queues; stdlib.md, testing.VirtualClock). Each seed
// generates a test whose body first calls functions that own resources
// (released last first, a parameter's handed over to its callee, a
// returned one kept by the caller), then, under a virtual clock, starts a
// consumer task that logs each value of a queue and producer tasks whose
// steps sleep, log, loop, push to the queue and own resources, and maybe
// a sleeper it stops. The body waits for the producers, ends the queue,
// waits for the consumer and stops the sleeper, then fails with the
// journal, which the test reads from the result. The model knows each
// line's virtual time and each task's lines in order; it checks that
// every task's lines come in its order, every line's time is the model's
// and the journal's times never fall (virtual time moves only when every
// task waits), every pushed value reaches the consumer in each
// producer's order, a stopped sleeper runs its exit action and nothing
// after its sleep, and the same seed gives the same journal and names
// the same schedule. A seed that leaves the queue open ends as a defect
// that names the waiting tasks. Over the seeds, two producers' lines must
// interleave at one instant at least once, so the schedule is the seed's
// and not the order the tasks started in. A failing seed prints the Ergo
// source, the result and the replay command.
//
// Mutants only it kills (testdata/mutants.json):
// oracle-o3-scheduler-host-order, oracle-o3-virtual-time-skips-wake,
// oracle-o3-deadlock-passes, oracle-o3-owned-release-forward,
// oracle-o3-owned-handoff-ignored, oracle-o3-stop-ignored.
func TestEvalTasksAgreeWithModel(t *testing.T) {
	n := 120
	if testing.Short() {
		n = 40
	}
	first := uint64(0)
	if s := os.Getenv("ERGO_EVAL_TASKS_SEED"); s != "" {
		v, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		first, n = v, 1
	}
	reached := map[string]bool{}
	for seed := first; seed < first+uint64(n); seed++ {
		g := &taskGen{r: rand.New(rand.NewPCG(seed, 0x7a)), reached: reached}
		src := g.program()
		if dir := os.Getenv("ERGO_EVAL_TASKS_DUMP"); dir != "" {
			if err := os.WriteFile(fmt.Sprintf("%s/seed%d.ergo", dir, seed), []byte(src), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		fail := func(format string, args ...any) {
			t.Helper()
			t.Fatalf("seed %d: %s\n%s\nreplay: ERGO_EVAL_TASKS_SEED=%d go test -run TestEvalTasksAgreeWithModel ./internal/sketch/eval",
				seed, fmt.Sprintf(format, args...), src, seed)
		}
		run := func() Result {
			f, err := syntax.Parse(src)
			if err != nil {
				fail("%v", err)
			}
			pkg, diags := check.Check("main", []*syntax.File{f}, nil)
			if len(diags) > 0 {
				fail("check: %v", diags)
			}
			rep, err := Run(pkg, DefaultBudget())
			if err != nil {
				fail("%v", err)
			}
			for _, res := range rep.Results {
				if res.Name == "Run" {
					return res
				}
			}
			fail("no obligation Run in %+v", rep.Results)
			return Result{}
		}
		res := run()
		if res.Schedule == 0 {
			fail("the result names no schedule: %+v", res)
		}
		if g.deadlock {
			if res.Status != Defect || !strings.Contains(res.Reason, "waits for a value in a queue") || !strings.Contains(res.Reason, "(the obligation) waits for task #2 to end") {
				fail("Run is %s (%s), not the defect of a deadlock", res.Status, res.Reason)
			}
			continue
		}
		if res.Status != Refuted || !strings.HasPrefix(res.Reason, "a failure escaped: main.Out{") {
			fail("Run is %s (%s), not the journal", res.Status, res.Reason)
		}
		journal := journalOf(res.Reason)
		if err := g.agrees(journal); err != "" {
			fail("%s\njournal:\n  %s", err, strings.Join(journal, "\n  "))
		}
		again := run()
		if again.Reason != res.Reason || again.Schedule != res.Schedule {
			fail("a second run of the seed differs:\n%s (schedule %d)\n%s (schedule %d)", res.Reason, res.Schedule, again.Reason, again.Schedule)
		}
	}
	if n > 1 {
		for _, k := range taskForms {
			if !reached[k] {
				t.Fatalf("the seeds never reached the form %s (reached %v)", k, reached)
			}
		}
	}
}

// taskForms lists every form a run of seeds must reach.
var taskForms = []string{"sleep", "say", "loop", "push", "own", "cap0", "capN", "stop", "deadlock", "interleaved"}

var quoted = regexp.MustCompile(`"(?:[^"\\]|\\.)*"`)

// journalOf reads the journal from the escaped Out's description.
func journalOf(reason string) []string {
	var out []string
	for _, q := range quoted.FindAllString(reason, -1) {
		s, err := strconv.Unquote(q)
		if err != nil {
			panic(err)
		}
		out = append(out, s)
	}
	return out
}

// taskLine is one line the model expects of a task: its text and the
// virtual time it is written at.
type taskLine struct {
	text string
	at   time.Duration
}

type taskGen struct {
	r       *rand.Rand
	reached map[string]bool
	b       strings.Builder
	// prefix is the journal of the owned part, before any task starts.
	prefix []string
	// tasks are the producers' lines, pushes the values each pushes and
	// pushedAt the time of each push.
	tasks    [][]taskLine
	pushes   [][]int
	pushedAt map[int]time.Duration
	sleeper  bool
	deadlock bool
}

func (g *taskGen) line(format string, args ...any) { fmt.Fprintf(&g.b, format+"\n", args...) }

const taskPrelude = `import queue
import scope
import sync
import task
import testing
import time

let journal = sync.NewRef[[]string]([]string{})

func say(s string) { journal.Update(func(xs []string) []string { xs.Append(s) }) }

type Res { name string }

func Close(r Res) { say("close " + r.name) }

func use(r Res) { say("use " + r.name) }

func take(r Res) takes r {
	use(r)
}

func lend(a string) {
	let x = Res{name: a}
	use(x)
	say("lent " + a)
}

func pair(a string, b string) Res {
	let x = Res{name: a}
	let y = Res{name: b}
	use(x)
	y
}

func two(a string, b string) {
	let x = Res{name: a}
	let y = Res{name: b}
	use(x)
	use(y)
}

func give(a string) {
	let x = Res{name: a}
	take(x)
	say("gave " + a)
}

func kept(a string, b string) {
	let k = pair(a, b)
	use(k)
}

// exits says its name when it ends, by an exit action.
func exits(name string) {
	scope.OnExit() { say(name) }
}

type Out { lines []string }

func emit() fails Out { return Out{lines: journal.Get()} }
`

func (g *taskGen) program() string {
	g.b.WriteString(taskPrelude)
	g.line("")
	g.line("test Run {")
	g.owned()
	capacity := g.r.IntN(3)
	if capacity == 0 {
		g.reached["cap0"] = true
	} else {
		g.reached["capN"] = true
	}
	g.deadlock = g.r.IntN(8) == 0
	g.sleeper = !g.deadlock && g.r.IntN(3) == 0
	g.line("\tlet c = testing.NewVirtualClock(0)")
	g.line("\tlet q = queue.New[int](%d)", capacity)
	g.line("\twith context { Scheduler: c, time.Clock: c } {")
	g.line("\t\tlet start = time.Clock.Now()")
	g.line("\t\tsay(\"start @\\(time.Clock.Now().Sub(start))\")")
	g.line("\t\tlet cons = task.Go() {")
	g.line("\t\t\tfor v in q.Values() {")
	g.line("\t\t\t\tsay(\"got \\(v)\")")
	g.line("\t\t\t}")
	g.line("\t\t}")
	g.pushedAt = map[int]time.Duration{}
	k := 1 + g.r.IntN(3)
	for i := range k {
		g.producer(i)
	}
	if g.sleeper {
		g.reached["stop"] = true
		g.line("\t\tlet z = task.Go() {")
		g.line("\t\t\tscope.OnExit() { say(\"z exit\") }")
		g.line("\t\t\tScheduler.Sleep(1h)")
		g.line("\t\t\tsay(\"z woke\")")
		g.line("\t\t}")
	}
	for i := range k {
		g.line("\t\tt%d.Wait()", i)
	}
	if g.deadlock {
		g.reached["deadlock"] = true
	} else {
		g.line("\t\tq.End()")
	}
	g.line("\t\tcons.Wait()")
	if g.sleeper {
		g.line("\t\tz.Stop()")
	}
	g.line("\t}")
	g.line("\ttry emit()")
	g.line("}")
	return g.b.String()
}

// owned calls the owning functions, each with the journal it writes.
func (g *taskGen) owned() {
	for i := range g.r.IntN(3) {
		g.reached["own"] = true
		a, b := fmt.Sprintf("o%da", i), fmt.Sprintf("o%db", i)
		switch g.r.IntN(4) {
		case 3:
			g.line("\tlend(%q)", a)
			g.prefix = append(g.prefix, "use "+a, "lent "+a, "close "+a)
		case 0:
			g.line("\ttwo(%q, %q)", a, b)
			g.prefix = append(g.prefix, "use "+a, "use "+b, "close "+b, "close "+a)
		case 1:
			g.line("\tgive(%q)", a)
			g.prefix = append(g.prefix, "use "+a, "close "+a, "gave "+a)
		case 2:
			g.line("\tkept(%q, %q)", a, b)
			g.prefix = append(g.prefix, "use "+a, "close "+a, "use "+b, "close "+b)
		}
	}
}

// producer writes task t<i>: its steps, each with the lines the model
// expects at its time.
func (g *taskGen) producer(i int) {
	var lines []taskLine
	var pushes []int
	var now time.Duration
	var res []string
	g.line("\t\tlet t%d = task.Go() {", i)
	for j := range 1 + g.r.IntN(4) {
		name := fmt.Sprintf("t%d.%d", i, j)
		switch g.r.IntN(5) {
		case 0:
			g.reached["sleep"] = true
			d := time.Duration(1+g.r.IntN(4)) * time.Millisecond
			now += d
			g.line("\t\t\tScheduler.Sleep(%dms)", d/time.Millisecond)
			g.line("\t\t\tsay(\"%s @\\(time.Clock.Now().Sub(start))\")", name)
			lines = append(lines, taskLine{fmt.Sprintf("%s @%s", name, now), now})
		case 1:
			g.reached["say"] = true
			g.line("\t\t\tsay(%q)", name)
			lines = append(lines, taskLine{name, now})
		case 2:
			g.reached["loop"] = true
			m := 2 + g.r.IntN(3)
			g.line("\t\t\tfor k in 0..%d {", m)
			g.line("\t\t\t\tsay(\"%s.\\(k)\")", name)
			g.line("\t\t\t}")
			for k := range m {
				lines = append(lines, taskLine{fmt.Sprintf("%s.%d", name, k), now})
			}
		case 3:
			g.reached["push"] = true
			v := 100*(i+1) + j
			g.line("\t\t\tq.Push(%d) catch {", v)
			g.line("\t\t\tcase queue.Closed:")
			g.line("\t\t\t}")
			pushes = append(pushes, v)
			g.pushedAt[v] = now
		case 4:
			g.reached["own"] = true
			r := name + ".r"
			g.line("\t\t\tlet r%d = Res{name: %q}", j, r)
			g.line("\t\t\tuse(r%d)", j)
			lines = append(lines, taskLine{"use " + r, now})
			res = append(res, r)
		}
	}
	// The task's owned locals are released last first when its body ends.
	for x := len(res) - 1; x >= 0; x-- {
		lines = append(lines, taskLine{"close " + res[x], now})
	}
	g.line("\t\t}")
	g.tasks = append(g.tasks, lines)
	g.pushes = append(g.pushes, pushes)
}

// agrees checks a journal against the model; it returns what is wrong.
func (g *taskGen) agrees(journal []string) string {
	if len(journal) < len(g.prefix) || strings.Join(journal[:len(g.prefix)], "|") != strings.Join(g.prefix, "|") {
		return fmt.Sprintf("the owned part is not %q", g.prefix)
	}
	rest := journal[len(g.prefix):]
	if len(rest) == 0 || rest[0] != "start @0s" {
		return "the virtual clock does not start at 0s"
	}
	rest = rest[1:]
	if g.sleeper {
		if len(rest) == 0 || rest[len(rest)-1] != "z exit" {
			return "the stopped sleeper's exit action is not the last line"
		}
		rest = rest[:len(rest)-1]
	}
	// owner maps each producer line to its task and index.
	type pos struct{ task, idx int }
	owner := map[string]pos{}
	for ti, lines := range g.tasks {
		for li, l := range lines {
			owner[l.text] = pos{ti, li}
		}
	}
	next := make([]int, len(g.tasks))
	got := make([][]int, len(g.tasks))
	var last time.Duration
	// segs are the last runs of one producer's lines, for the a, b, a
	// of two producers interleaving at one instant.
	type seg struct {
		task int
		at   time.Duration
	}
	var segs []seg
	for _, l := range rest {
		var at time.Duration
		if v, ok := strings.CutPrefix(l, "got "); ok {
			n, err := strconv.Atoi(v)
			if err != nil {
				return "a bad line " + l
			}
			at0, ok := g.pushedAt[n]
			if !ok {
				return "a value no producer pushed: " + l
			}
			at = at0
			got[n/100-1] = append(got[n/100-1], n)
		} else {
			p, ok := owner[l]
			if !ok {
				return "a line the model has not: " + l
			}
			if p.idx != next[p.task] {
				return fmt.Sprintf("task t%d's line %q is out of its order", p.task, l)
			}
			next[p.task]++
			at = g.tasks[p.task][p.idx].at
			if n := len(segs); n == 0 || segs[n-1] != (seg{p.task, at}) {
				segs = append(segs, seg{p.task, at})
				if n := len(segs); n >= 3 && segs[n-3] == segs[n-1] && segs[n-2].at == at {
					g.reached["interleaved"] = true
				}
			}
		}
		if at < last {
			return fmt.Sprintf("the line %q at %s comes after a line at %s", l, at, last)
		}
		last = at
	}
	for ti, lines := range g.tasks {
		if next[ti] != len(lines) {
			return fmt.Sprintf("task t%d wrote %d of its %d lines", ti, next[ti], len(lines))
		}
		if fmt.Sprint(got[ti]) != fmt.Sprint(g.pushes[ti]) {
			return fmt.Sprintf("the consumer got %v of task t%d, which pushed %v", got[ti], ti, g.pushes[ti])
		}
	}
	return ""
}
