package lower

import (
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/eval"
)

// rtShapes are the ways a returned value is made while its frame may own
// other values (check.Info.ReturnedTemps): a call, a method on an owned
// value, an if arm, a match arm, a try, and a pattern binding handed into
// the returned Option by a match arm of a union or by the one arm of a
// record (lane MODES' repro-tail-arm).
var rtShapes = []string{"call", "method", "if", "match", "try", "bindUnion", "bindRecord"}

// rtPrelude is every generated program's declarations: a journal of opens
// and closes, the resource, a value that owns one (Fac), and the unions
// the arms and the bindings match.
const rtPrelude = `import sync

type Res { id int } implements Closer
type Bad { n int }
type Fac { r Res }
type PA {}
type PB {}
type Pick = PA | PB
type gP { V Res }
type gE {}
type gU = gP | gE

let journal = sync.NewRef[[]string]([]string{})

func say(s string) { journal.Update(func(xs []string) []string { xs.Append(s) }) }

func Close(r Res) { say("close \(r.id)") }

func open(id int) Res {
	say("open \(id)")
	Res{id: id}
}

func openf(id int, n int) Res fails Bad {
	if n > 0 {
		return Bad{n: n}
	}
	open(id)
}

func Make(f Fac, id int) Res { open(id) }

func finish(r own Option[Res]) {
	match r {
	case Some{Value: x}:
		Close(x)
	case None:
	}
}

func main() {}
`

// rtCase is one generated function, its caller's test and the journal
// the ownership rules give (resources R9, R10): the frame's values are
// released last first when it returns, the returned value only by the
// test after "picked".
type rtCase struct {
	name     string
	decls    string
	test     string
	features []string
}

// rtGen draws the cases of one program.
type rtGen struct {
	r  *rand.Rand
	id int
}

func (g *rtGen) next() int { g.id++; return g.id }

// rtFunc is one function that returns a value made by shape while it
// holds held values; it returns its source, the call the test makes, the
// journal up to the return and the id of the returned resource.
func (g *rtGen) rtFunc(name, shape string, held int, ret bool) (src, call string, events []string, rid int, option bool) {
	var b strings.Builder
	var opened, closes []string
	params, args := "()", "()"
	switch shape {
	case "bindUnion", "bindRecord":
		option = true
	}
	rid = g.next()
	var argOpen []string
	switch shape {
	case "if":
		params, args = "(sel bool)", fmt.Sprintf("(%v)", g.r.IntN(2) == 0)
	case "match":
		if g.r.IntN(2) == 0 {
			args = "(PA)"
		} else {
			args = "(PB)"
		}
		params = "(p Pick)"
	case "bindUnion":
		params, args = "(o own gU)", fmt.Sprintf("(gUOf(%d))", rid)
		argOpen = append(argOpen, fmt.Sprintf("open %d", rid))
	case "bindRecord":
		params, args = "(o own gP)", fmt.Sprintf("(gP{V: open(%d)})", rid)
		argOpen = append(argOpen, fmt.Sprintf("open %d", rid))
	}
	result := "Res"
	if option {
		result = "Option[Res]"
	}
	if shape == "try" {
		result = "Res fails Bad"
	}
	fmt.Fprintf(&b, "func %s%s %s {\n", name, params, result)
	if shape == "method" && held == 0 {
		held = 1
	}
	fac := -1
	for j := 0; j < held; j++ {
		h := g.next()
		if shape == "method" && j == 0 || g.r.IntN(3) == 0 {
			fmt.Fprintf(&b, "\tlet h%d = Fac{r: open(%d)}\n", j, h)
			if fac < 0 {
				fac = j
			}
		} else {
			fmt.Fprintf(&b, "\tlet h%d = open(%d)\n", j, h)
		}
		opened = append(opened, fmt.Sprintf("open %d", h))
		closes = append([]string{fmt.Sprintf("close %d", h)}, closes...)
	}
	var x string
	switch shape {
	case "call":
		x = fmt.Sprintf("open(%d)", rid)
	case "method":
		x = fmt.Sprintf("h%d.Make(%d)", fac, rid)
	case "if":
		x = fmt.Sprintf("if sel {\n\t\topen(%d)\n\t} else {\n\t\topen(%d)\n\t}", rid, rid)
	case "match":
		x = fmt.Sprintf("match p {\n\tcase PA:\n\t\topen(%d)\n\tcase PB:\n\t\topen(%d)\n\t}", rid, rid)
	case "try":
		x = fmt.Sprintf("try openf(%d, 0)", rid)
	case "bindUnion":
		x = "match o {\n\tcase gP{V: v}:\n\t\tSome{Value: v}\n\tcase gE:\n\t\tNone\n\t}"
	case "bindRecord":
		x = "match o {\n\tcase gP{V: v}:\n\t\tSome{Value: v}\n\t}"
	}
	if ret {
		fmt.Fprintf(&b, "\treturn %s\n}\n", x)
	} else {
		fmt.Fprintf(&b, "\t%s\n}\n", x)
	}
	events = append(events, argOpen...)
	events = append(events, opened...)
	if !option {
		events = append(events, fmt.Sprintf("open %d", rid))
	}
	events = append(events, closes...)
	return b.String(), name + args, events, rid, option
}

// rtDraw draws case i: a function of a shape, the values it holds, its
// return (a return statement or the body's tail), and for a Res it
// returns, maybe a wrapper that holds a value of its own and returns the
// call (a returned new value of a call, one frame out).
func (g *rtGen) rtDraw(i int) rtCase {
	shape := rtShapes[g.r.IntN(len(rtShapes))]
	held := g.r.IntN(3)
	ret := g.r.IntN(2) == 0
	name := fmt.Sprintf("f%d", i)
	src, call, events, rid, option := g.rtFunc(name, shape, held, ret)
	c := rtCase{name: fmt.Sprintf("T%d", i), decls: src}
	pos := "tail"
	if ret {
		pos = "return"
	}
	c.features = append(c.features, "shape:"+shape, "pos:"+pos, fmt.Sprintf("held:%d", min(held, 1)))
	if !option && g.r.IntN(2) == 0 {
		w := fmt.Sprintf("w%d", i)
		gid := g.next()
		result := "Res"
		inner := call
		if shape == "try" {
			result, inner = "Res fails Bad", "try "+call
		}
		c.decls += fmt.Sprintf("\nfunc %s() %s {\n\tlet g = open(%d)\n\treturn %s\n}\n", w, result, gid, inner)
		events = append(append([]string{fmt.Sprintf("open %d", gid)}, events...), fmt.Sprintf("close %d", gid))
		call = w + "()"
		c.features = append(c.features, "wrap")
	}
	if shape == "try" {
		call = "try " + call
	}
	var t strings.Builder
	fmt.Fprintf(&t, "test %s {\n\tjournal.Set([]string{})\n\tlet r = %s\n\tsay(\"picked\")\n", c.name, call)
	before := append(events, "picked")
	fmt.Fprintf(&t, "\tassert journal.Get() == %s\n", rtList(before))
	if option {
		t.WriteString("\tfinish(r)\n")
	} else {
		t.WriteString("\tClose(r)\n")
	}
	fmt.Fprintf(&t, "\tassert journal.Get() == %s\n}\n", rtList(append(before, fmt.Sprintf("close %d", rid))))
	c.test = t.String()
	return c
}

func rtList(xs []string) string {
	q := make([]string, len(xs))
	for i, x := range xs {
		q[i] = fmt.Sprintf("%q", x)
	}
	return "[]string{" + strings.Join(q, ", ") + "}"
}

// rtProgram is seed's program: rtPerProgram drawn cases.
func rtProgram(seed uint64) (string, []rtCase) {
	g := &rtGen{r: rand.New(rand.NewPCG(seed, 0x5265747572)), id: 0}
	var b strings.Builder
	b.WriteString(rtPrelude)
	b.WriteString("\nfunc gUOf(id int) gU {\n\tgP{V: open(id)}\n}\n")
	var cases []rtCase
	for i := 0; i < rtPerProgram; i++ {
		c := g.rtDraw(i)
		cases = append(cases, c)
		b.WriteString("\n" + c.decls + "\n" + c.test)
	}
	return b.String(), cases
}

const (
	rtSeeds      = 8
	rtPerProgram = 8
)

// rtTaskRepro is the smallest repro of the evaluator's fixed bug
// (EVAL-RETURNED-TEMPS, found by STOP-NESTED-WAIT): a function that
// returns `outer.Wait()`, a Task another task started, while its frame
// still owns outer. The evaluator released the returned Task when the
// frame ended, so the Task was stopped before it ran and the Pop below
// waited forever.
const rtTaskRepro = `import queue
import task
import time

func handed(up queue.Queue[int], gate queue.Queue[int]) Task[int, queue.Closed] needs Scheduler {
	let outer = task.Go() {
		task.Go() {
			up.Offer(1) catch {
			case _:
			}
			try gate.Pop()
		}
	}
	return outer.Wait()
}

func main() {}

test ReturnedTaskRuns {
	let up = queue.New[int](1)
	let gate = queue.New[int](0)
	with context { Scheduler: time.NewManual(0) } {
		let b = task.Go() {
			let inner = handed(up, gate)
			let _ = try up.Pop()
			inner.Stop()
			0
		}
		let _ = b.Wait() catch {
		case _:
			0
		}
	}
	assert up.Len() == 0
}
`

// TestReturnedTempsAgree: a function that returns a new value while its
// frame owns another (check.Info.ReturnedTemps; resources R9, R10) gives
// the same journal of opens and closes on the text lowering (built and
// run), on the IR path (ergo test) and in the evaluator, and it is the
// journal the rules give: the frame's own values close last first at the
// return, the returned value only when its receiver closes it. Seeds draw
// the shape (rtShapes), how many values the frame holds, a return
// statement or the body's tail, and a wrapper that returns the call one
// frame out; the batch must reach every production. The fixed bug's
// smallest repro, rtTaskRepro (`return outer.Wait()`), runs on all three
// paths too. A failing seed prints its source and the replay command
// (ERGO_SEED=n).
//
// Mutant: eval-returned-temp-released (internal/sketch/eval
// mutants.json): the evaluator releases the returned value at the frame's
// end, which closes it before "picked" and again in the test (a double
// close).
//
// It builds Go because two of the three verdicts are the built Go's: the
// text lowering's test binary and the IR path's ergo test; go/types cannot
// run a release.
func TestReturnedTempsAgree(t *testing.T) {
	needsBuild(t)
	first, n := hrSeeds(t, rtSeeds)
	type prog struct {
		seed  int
		src   string
		tests []string
	}
	var progs []prog
	covered := map[string]bool{}
	for seed := first; seed < first+n; seed++ {
		src, cases := rtProgram(uint64(seed))
		p := prog{seed: seed, src: src}
		for _, c := range cases {
			p.tests = append(p.tests, c.name)
			for _, f := range c.features {
				covered[f] = true
			}
		}
		progs = append(progs, p)
	}
	progs = append(progs, prog{seed: -1, src: rtTaskRepro, tests: []string{"ReturnedTaskRuns"}})
	if n > 1 {
		var missing []string
		for _, s := range rtShapes {
			for _, f := range []string{"shape:" + s} {
				if !covered[f] {
					missing = append(missing, f)
				}
			}
		}
		for _, f := range []string{"pos:return", "pos:tail", "held:0", "held:1", "wrap"} {
			if !covered[f] {
				missing = append(missing, f)
			}
		}
		sort.Strings(missing)
		if len(missing) > 0 {
			t.Fatalf("the seeds reach no %s", strings.Join(missing, ", "))
		}
	}
	var builds []*lowered
	for _, p := range progs {
		builds = append(builds, lowerTestBuild(t, rtName(p.seed), p.src))
	}
	exes := buildAll(t, builds, true)
	if exes == nil {
		return
	}
	for _, p := range progs {
		fail := func(format string, args ...any) {
			t.Helper()
			replay := fmt.Sprintf("ERGO_SEED=%d go test -run '^TestReturnedTempsAgree$' ./internal/sketch/lower", p.seed)
			if p.seed < 0 {
				replay = "go test -run '^TestReturnedTempsAgree$' ./internal/sketch/lower (rtTaskRepro)"
			}
			t.Errorf("%s: %s\n--- source\n%s\nreplay: %s", rtName(p.seed), fmt.Sprintf(format, args...), p.src, replay)
		}
		out, stderr, code := runExeArgs(t, exes[rtName(p.seed)])
		if bad := rtMissing(out, p.tests); code != 0 || len(bad) > 0 {
			fail("the text lowering: exit %d, not ok: %v\n%s%s", code, bad, firstLines(out, 30), firstLines(stderr, 10))
			continue
		}
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "main.ergo"), []byte(p.src), 0o644); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(ergoCLI(t), "test", dir)
		cmd.Dir = t.TempDir()
		got, err := cmd.CombinedOutput()
		if bad := rtMissing(string(got), p.tests); err != nil || len(bad) > 0 {
			fail("the IR path (ergo test): %v, not ok: %v\n%s", err, bad, firstLines(string(got), 30))
			continue
		}
		rep, err := evaluate(p.src)
		if err != nil {
			fail("evaluate: %v", err)
			continue
		}
		passed := map[string]bool{}
		for _, res := range rep.Results {
			if res.Status == eval.Passed {
				passed[res.Name] = true
			} else {
				fail("the evaluator: %s %s: %s", res.Name, res.Status, firstLines(fmt.Sprint(res), 4))
			}
		}
		for _, name := range p.tests {
			if !passed[name] {
				fail("the evaluator did not pass %s", name)
			}
		}
	}
}

func rtName(seed int) string {
	if seed < 0 {
		return "rettemps_task"
	}
	return fmt.Sprintf("rettemps_%d", seed)
}

// rtMissing is the tests of names that out does not report ok.
func rtMissing(out string, names []string) []string {
	ok := map[string]bool{}
	for _, l := range lines(out) {
		if r, found := strings.CutPrefix(l, "ok "); found {
			ok[strings.Fields(r)[0]] = true
		}
	}
	var bad []string
	for _, n := range names {
		if !ok[n] {
			bad = append(bad, n)
		}
	}
	return bad
}
