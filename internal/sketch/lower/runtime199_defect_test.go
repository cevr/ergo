package lower

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"math/rand/v2"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// Fork-tree defects (runtime-199 stage 4b; runtime.md, G2).

// TestRuntime199DefectProperty generates fork trees and checks each defect
// report against a model (runtime.md, G2). A case is a chain of tasks of
// depth 1 to 3, or 33, past the 32 tasks a chain keeps; the deepest task
// fails. At each level the parent:
//
//   - starts the next task directly or through a function that returns it
//     (the Task moves to the caller);
//   - maybe opens a context region around the start, which the child
//     inherits, and one after it, around the observation, which it must
//     not see;
//   - starts siblings first: one whose typed failure a catch handles while
//     the run goes on, and one blocked in a catch-wrapped Pop, which the
//     parent's drain interrupts and whose catch must not handle the
//     interruption;
//   - meets the child's exit by Wait inside a catch of the typed row, by
//     Stop, or by its own drain after a typed failure of its own.
//
// The failing task's defect is in its body, in an exit action, in an exit
// action while a body defect unwinds, or in an exit action after a typed
// failure; it may run inside a region of its own. Depth 0 is the root
// task's own defect, which writes main's chain line only under context. A
// boundary case's failing task calls a function with no exit action, so
// the task boundary, not a frame, meets the panic. A Stop or a drain drops
// a child's typed failure and keeps its defects; a Wait keeps every reason,
// and the catch never handles one that holds a defect.
//
// Each case runs natively, untraced and traced. The report must equal the
// model's: every reason survives, and each defect names the failing task's
// own Ergo frames (never the observer's), its chain from the failing task
// to main with each fork's site, cut at 32 tasks, and the context in force
// in the failing task at the defect, not its parent's later state. The
// traced report equals the untraced one, the status is 2, and the exit
// holds no task, so ancestry outlives no task.
func TestRuntime199DefectProperty(t *testing.T) {
	needsBuild(t)
	cases := dfCases()
	f := &dfFile{}
	for _, l := range []string{"import task", "import queue", "import scope", "import log", "import trace", "", "type Fault {}", "", "func main() {}"} {
		f.add("%s", l)
	}
	for _, c := range cases {
		c.write(f)
	}
	o := mustLower(t, "defects", strings.Join(f.lines, "\n")+"\n")
	got := dfNative(t, o, cases)
	shapes := map[string]bool{}
	for _, c := range cases {
		c.cover(shapes)
		r, ok := got[c.name()]
		if !ok {
			t.Errorf("%s: no native result", c.name())
			continue
		}
		if r.Status != 2 {
			t.Errorf("%s: status %d, want 2", c.name(), r.Status)
		}
		if r.Holds {
			t.Errorf("%s: the exit holds a task", c.name())
		}
		norm := dfNorm(r.Report)
		if traced := dfNorm(r.Traced); traced != norm {
			t.Errorf("%s: traced report:\n%s\nuntraced:\n%s", c.name(), traced, norm)
		}
		for _, chain := range dfChains(r.Report) {
			for i := 1; i < len(chain); i++ {
				if chain[i] >= chain[i-1] {
					t.Errorf("%s: chain task numbers %v do not go from child to parent", c.name(), chain)
				}
			}
		}
		want := c.expect()
		if have := dfBlocks(norm); !slices.Equal(have, want) {
			t.Errorf("%s: %s\nreport:\n%s\nwant blocks:\n%s", c.name(), c.describe(), r.Report, strings.Join(want, ""))
		}
	}
	for _, s := range []string{"depth 0", "root with context", "root without context", "task boundary",
		"depth 1", "depth 2", "depth 3", "chain cut at 32", "moved", "region before",
		"region after", "shadowed", "own region", "caught sibling", "blocked sibling", "two siblings",
		"failure reaches main", "body", "release", "body+release", "failure+release"} {
		if !shapes[s] {
			t.Errorf("no case has the shape %q", s)
		}
	}
	for _, at := range []string{"failing", "upper"} {
		for _, ob := range dfObsNames {
			if !shapes[at+" "+ob] {
				t.Errorf("no case meets a %s task by %s", at, ob)
			}
		}
	}
	t.Logf("%d generated fork trees, each run untraced and traced", len(cases))
}

// How a parent meets its child's exit.
const (
	dfWait = iota
	dfStop
	dfDrain
)

var dfObsNames = []string{"Wait", "Stop", "drain"}

// Where the failing task's defect is.
const (
	dfBody        = iota // the body panics
	dfRelease            // an exit action panics after a normal body
	dfBodyRelease        // the body panics, then an exit action does
	dfFailRelease        // the body fails, then an exit action panics
)

var dfPlaceNames = []string{"body", "release", "body+release", "failure+release"}

// dfCtx is a context value: key a, b or own through log.Annotate, or
// span through trace.Span.
type dfCtx struct{ key, text string }

func (c *dfCtx) operand() string {
	if c.key == "span" {
		return fmt.Sprintf("trace.Span(%q)", c.text)
	}
	return fmt.Sprintf("log.Annotate(%q, %q)", c.key, c.text)
}

// pkg is the namespace a report prints: a std package's ("std:" and its
// path; contextNamespace, C30).
func (c *dfCtx) pkg() string {
	if c.key == "span" {
		return "std:trace"
	}
	return "std:log"
}

// dfLevel is how a parent starts and meets the next task of the chain.
type dfLevel struct {
	obs           int
	moved         bool
	before, later *dfCtx
	sibs          []string // "caught" or "blocked", in start order
	site          string   // the fork's site, set by write
}

type dfCase struct {
	k, depth int
	levels   []dfLevel // levels[j]: level j starts level j+1
	own      *dfCtx    // the failing task's own region around its body
	place    int
	// boundary: the failing task's block calls FB, a function with no exit
	// action, so no frame of the task recovers the fresh defect before the
	// task boundary does (guarded). Depth 0 is the root task's own defect.
	boundary bool
	// Set by write: the failing task's frames.
	bodyAt, bodyCall, releaseAt, releaseCall, lastAt, block string
}

func (c *dfCase) name() string { return fmt.Sprintf("run%d", c.k) }

func (c *dfCase) describe() string {
	var b strings.Builder
	fmt.Fprintf(&b, "depth %d, %s, boundary %v", c.depth, dfPlaceNames[c.place], c.boundary)
	for j, lv := range c.levels {
		fmt.Fprintf(&b, "; level %d: %s, moved %v, siblings %v", j, dfObsNames[lv.obs], lv.moved, lv.sibs)
		if lv.before != nil {
			fmt.Fprintf(&b, ", before %s", lv.before.operand())
		}
		if lv.later != nil {
			fmt.Fprintf(&b, ", later %s", lv.later.operand())
		}
	}
	if c.own != nil {
		fmt.Fprintf(&b, "; own %s", c.own.operand())
	}
	return b.String()
}

// dfCases enumerates depth 1 to 3 x the failing task's observation x the
// defect place, draws the other axes from a fixed seed, and adds chains of
// depth 33, the root task's own defect at every place, with and without
// context, and a defect at the task boundary at depth 1 to 3.
func dfCases() []*dfCase {
	r := rand.New(rand.NewPCG(199, 4))
	keys := []string{"a", "b", "span"}
	var cases []*dfCase
	add := func(depth, last, place int, boundary bool) *dfCase {
		c := &dfCase{k: len(cases), depth: depth, place: place, boundary: boundary}
		for j := range depth {
			lv := dfLevel{obs: r.IntN(3), moved: r.IntN(3) == 0}
			if j == depth-1 {
				lv.obs = last
			}
			if r.IntN(2) == 0 {
				lv.before = &dfCtx{keys[r.IntN(3)], fmt.Sprintf("v%d_%d", c.k, j)}
			}
			if r.IntN(3) == 0 {
				lv.later = &dfCtx{keys[r.IntN(3)], fmt.Sprintf("later%d_%d", c.k, j)}
			}
			if depth < 33 {
				for range r.IntN(3) {
					lv.sibs = append(lv.sibs, []string{"caught", "blocked"}[r.IntN(2)])
				}
			}
			c.levels = append(c.levels, lv)
		}
		if r.IntN(2) == 0 {
			c.own = &dfCtx{[]string{"a", "own"}[r.IntN(2)], fmt.Sprintf("own%d", c.k)}
		}
		cases = append(cases, c)
		return c
	}
	for depth := 1; depth <= 3; depth++ {
		for last := range 3 {
			for place := range 4 {
				add(depth, last, place, false)
			}
		}
	}
	add(33, dfWait, dfBody, false)
	add(33, dfStop, dfFailRelease, false)
	for place := range 4 {
		for _, own := range []bool{false, true} {
			c := add(0, 0, place, false)
			c.own = nil
			if own {
				c.own = &dfCtx{keys[place%3], fmt.Sprintf("own%d", c.k)}
			}
		}
	}
	// The boundary task's parent waits: FB closes no queue the parent could
	// meet first, and a Stop could beat the panic.
	for depth := 1; depth <= 3; depth++ {
		add(depth, dfWait, dfBody, true)
	}
	return cases
}

// dfFile is the generated source, one line at a time.
type dfFile struct{ lines []string }

// add appends a line and returns its number.
func (f *dfFile) add(format string, a ...any) int {
	f.lines = append(f.lines, fmt.Sprintf(format, a...))
	return len(f.lines)
}

// at is the position FILE:LINE:COL of the first what on line n.
func (f *dfFile) at(n int, what string) string {
	col := strings.Index(f.lines[n-1], what)
	if col < 0 {
		panic(fmt.Sprintf("%q is not on line %d: %q", what, n, f.lines[n-1]))
	}
	return fmt.Sprintf("%s:%d:%d", testSourcePath, n, col+1)
}

// catch writes `let NAME = EXPR catch { case ERR: VALUE }` over lines,
// inside the region in when it is not nil.
func (f *dfFile) catch(name, expr, err, value string, in *dfCtx) {
	tab := "\t"
	if in != nil {
		f.add("\tlet %s = with %s {", name, in.operand())
		tab = "\t\t"
		f.add("%s%s catch {", tab, expr)
	} else {
		f.add("\tlet %s = %s catch {", name, expr)
	}
	f.add("%scase %s:", tab, err)
	f.add("%s\t%s", tab, value)
	f.add("%s}", tab)
	if in != nil {
		f.add("\t}")
	}
}

// panics writes a function that panics with index i and returns the
// frame of its panic.
func (f *dfFile) panics(name string, i int) string {
	f.add("func %s() int64 {", name)
	f.add("\tlet xs = []int64{1}")
	n := f.add("\txs[%d]", i)
	f.add("}")
	return fmt.Sprintf("%s at %s:%d", name, testSourcePath, n)
}

func (c *dfCase) write(f *dfFile) {
	k := c.k
	c.bodyAt = f.panics(fmt.Sprintf("B%d", k), 3)
	c.releaseAt = f.panics(fmt.Sprintf("R%d", k), 4)
	f.panics(fmt.Sprintf("BAD%d", k), 9)
	f.add("func FS%d() int64 fails Fault {", k)
	f.add("\treturn Fault")
	f.add("}")
	f.add("func BL%d(never queue.Queue[int]) int64 {", k)
	f.catch("_", "never.Pop()", "queue.Closed", "0", nil)
	f.add("\tBAD%d()", k)
	f.add("}")
	fn := fmt.Sprintf("F%d", k)
	f.add("func %s(done queue.Queue[int]) int64 fails Fault {", fn)
	f.add("\tscope.OnExit() {")
	f.add("\t\tdone.End()")
	f.add("\t}")
	if c.place != dfBody {
		f.add("\tscope.OnExit() {")
		n := f.add("\t\tR%d()", k)
		c.releaseCall = fmt.Sprintf("%s calls R%d at %s", fn, k, f.at(n, fmt.Sprintf("R%d(", k)))
		f.add("\t}")
	}
	var n int
	switch c.place {
	case dfBody, dfBodyRelease:
		n = f.add("\tB%d()", k)
		c.bodyCall = fmt.Sprintf("%s calls B%d at %s", fn, k, f.at(n, fmt.Sprintf("B%d(", k)))
	case dfRelease:
		n = f.add("\t0")
	case dfFailRelease:
		n = f.add("\treturn Fault")
	}
	c.lastAt = fmt.Sprintf("%s at %s:%d", fn, testSourcePath, n)
	f.add("}")
	if c.boundary {
		// FB opens no frame scope: only the task boundary meets its panic.
		fb := fmt.Sprintf("FB%d", k)
		f.add("func %s(done queue.Queue[int]) int64 fails Fault {", fb)
		n = f.add("\tB%d()", k)
		c.bodyCall = fmt.Sprintf("%s calls B%d at %s", fb, k, f.at(n, fmt.Sprintf("B%d(", k)))
		f.add("}")
	}
	if c.depth == 0 {
		// The root task's own defect: no fork, chain main alone.
		call := fmt.Sprintf("try %s(d)", fn)
		if c.own != nil {
			call = fmt.Sprintf("with %s { %s }", c.own.operand(), call)
		}
		f.add("func %s() int64 fails Fault {", c.name())
		f.add("\tlet d = queue.New[int](0)")
		n = f.add("\t%s", call)
		c.block = c.name() + " calls " + fn + " at " + f.at(n, fn+"(")
		f.add("}")
	}
	for j := c.depth - 1; j >= 0; j-- {
		c.writeLevel(f, j)
	}
}

// writeLevel writes level j, which starts level j+1 and meets its exit.
func (c *dfCase) writeLevel(f *dfFile, j int) {
	k, lv := c.k, &c.levels[j]
	name, params := fmt.Sprintf("L%d_%d", k, j), "done queue.Queue[int]"
	if j == 0 {
		name, params = c.name(), ""
	}
	callee := fmt.Sprintf("L%d_%d", k, j+1)
	if j+1 == c.depth {
		callee = fmt.Sprintf("F%d", k)
		if c.boundary {
			callee = fmt.Sprintf("FB%d", k)
		}
	}
	call := fmt.Sprintf("try %s(d)", callee)
	if j+1 == c.depth && c.own != nil {
		call = fmt.Sprintf("with %s { %s }", c.own.operand(), call)
	}
	block := "task.Go() { " + call + " }"
	// fork records the site of the task.Go on line n of forker.
	fork := func(forker string, n int) {
		lv.site = forker + " calls task.Go at " + f.at(n, "task.Go")
		if j+1 == c.depth {
			c.block = forker + " calls " + callee + " at " + f.at(n, callee+"(")
		}
	}
	start := block
	if lv.moved {
		forker := fmt.Sprintf("S%d_%d", k, j)
		f.add("func %s(d queue.Queue[int]) Task[int64, Fault] {", forker)
		fork(forker, f.add("\t%s", block))
		f.add("}")
		start = forker + "(d)"
	}
	f.add("func %s(%s) int64 fails Fault {", name, params)
	if j > 0 {
		f.add("\tscope.OnExit() {")
		f.add("\t\tdone.End()")
		f.add("\t}")
	}
	var blocked []int
	for i, s := range lv.sibs {
		switch s {
		case "caught":
			f.add("\tlet s%d = task.Go() { try FS%d() }", i, k)
			f.catch(fmt.Sprintf("c%d", i), fmt.Sprintf("s%d.Wait()", i), "Fault", "7", nil)
			f.add("\tif c%d != 7 {", i)
			f.add("\t\tBAD%d()", k)
			f.add("\t}")
		case "blocked":
			f.add("\tlet n%d = queue.New[int](0)", i)
			f.add("\tlet s%d = task.Go() { BL%d(n%d) }", i, k, i)
			blocked = append(blocked, i)
		}
	}
	f.add("\tlet d = queue.New[int](0)")
	if lv.before != nil {
		start = fmt.Sprintf("with %s { %s }", lv.before.operand(), start)
	}
	n := f.add("\tlet t = %s", start)
	if !lv.moved {
		fork(name, n)
	}
	if j+1 < c.depth || !c.boundary {
		f.catch("_", "d.Pop()", "queue.Closed", "0", nil)
	}
	result := "0"
	switch lv.obs {
	case dfWait:
		f.catch("r", "t.Wait()", "Fault", "-1", lv.later)
		result = "r"
	case dfStop:
		if lv.later != nil {
			f.add("\twith %s { t.Stop() }", lv.later.operand())
		} else {
			f.add("\tt.Stop()")
		}
	case dfDrain:
		f.add("\tlet f = task.Go() { try FS%d() }", k)
		if lv.later != nil {
			f.add("\twith %s { let _ = try f.Wait() }", lv.later.operand())
		} else {
			f.add("\tlet _ = try f.Wait()")
		}
		f.add("\tlet _ = try t.Wait()")
	}
	for _, i := range blocked {
		f.add("\tlet _ = s%d.Wait()", i)
	}
	f.add("\t%s", result)
	f.add("}")
}

// expect is the model's report: its blocks, each a reason as report
// writes it with task numbers as #N, sorted.
func (c *dfCase) expect() []string {
	const failure = "error: Fault{}\n"
	// The failing task's chain and context.
	var tail strings.Builder
	var sites []string
	for j := c.depth - 1; j >= 0; j-- {
		sites = append(sites, c.levels[j].site)
	}
	sites = append(sites, "main")
	cut := len(sites) > 32
	if cut {
		sites = sites[:32]
	}
	var in []*dfCtx
	for _, lv := range c.levels {
		if lv.before != nil {
			in = append(in, lv.before)
		}
	}
	if c.own != nil {
		in = append(in, c.own)
	}
	var shown []string
	for i, b := range in {
		shadowed := false
		for _, later := range in[i+1:] {
			shadowed = shadowed || later.key == b.key
		}
		if !shadowed {
			shown = append(shown, fmt.Sprintf(" %s.%s=%q", b.pkg(), b.key, b.text))
		}
	}
	if len(sites) == 1 && len(shown) == 0 {
		sites = nil // main alone, with no context, writes no chain
	}
	for i, s := range sites {
		fmt.Fprintf(&tail, "\ttask #N %s\n", s)
		if i == 0 && len(shown) > 0 {
			fmt.Fprintf(&tail, "\t  context%s\n", strings.Join(shown, ""))
		}
	}
	if cut {
		tail.WriteString("\t... (the chain is cut at 32 tasks)\n")
	}
	frames := func(fs ...string) string {
		var b strings.Builder
		for _, s := range fs {
			b.WriteString("\t" + s + "\n")
		}
		return b.String()
	}
	body := "panic: runtime error: index out of range [3] with length 1\n" +
		frames(c.bodyAt, c.bodyCall, c.block) + tail.String()
	release := "panic: runtime error: index out of range [4] with length 1 (in an exit action)\n"
	var exit []string
	switch c.place {
	case dfBody:
		exit = []string{body}
	case dfRelease, dfFailRelease:
		exit = []string{release + frames(c.releaseAt, c.releaseCall, c.lastAt, c.block) + tail.String()}
		if c.place == dfFailRelease {
			exit = append(exit, failure)
		}
	case dfBodyRelease:
		exit = []string{body, release + frames(c.releaseAt, c.releaseCall, c.bodyAt, c.bodyCall, c.block) + tail.String()}
	}
	for j := c.depth - 1; j >= 0; j-- {
		if c.levels[j].obs == dfWait {
			continue // every reason reaches the waiter; no catch takes a defect
		}
		exit = slices.DeleteFunc(exit, func(s string) bool { return s == failure })
		if c.levels[j].obs == dfDrain {
			exit = append(exit, failure) // the parent's own typed failure
		}
	}
	slices.Sort(exit)
	return exit
}

func (c *dfCase) cover(shapes map[string]bool) {
	shapes[fmt.Sprintf("depth %d", c.depth)] = true
	shapes["root with context"] = shapes["root with context"] || c.depth == 0 && c.own != nil
	shapes["root without context"] = shapes["root without context"] || c.depth == 0 && c.own == nil
	shapes["task boundary"] = shapes["task boundary"] || c.boundary
	shapes["chain cut at 32"] = shapes["chain cut at 32"] || c.depth >= 32
	shapes[dfPlaceNames[c.place]] = true
	shapes["own region"] = shapes["own region"] || c.own != nil
	keys := map[string]bool{}
	for j, lv := range c.levels {
		at := "upper"
		if j == c.depth-1 {
			at = "failing"
		}
		shapes[at+" "+dfObsNames[lv.obs]] = true
		shapes["moved"] = shapes["moved"] || lv.moved
		shapes["region after"] = shapes["region after"] || lv.later != nil
		shapes["two siblings"] = shapes["two siblings"] || len(lv.sibs) >= 2
		for _, s := range lv.sibs {
			shapes[s+" sibling"] = true
		}
		if lv.before != nil {
			shapes["region before"] = true
			shapes["shadowed"] = shapes["shadowed"] || keys[lv.before.key]
			keys[lv.before.key] = true
		}
	}
	if c.own != nil && keys[c.own.key] {
		shapes["shadowed"] = true
	}
	for _, b := range c.expect() {
		shapes["failure reaches main"] = shapes["failure reaches main"] || strings.HasPrefix(b, "error:")
	}
}

var dfTaskNumber = regexp.MustCompile(`task #\d+`)

// dfNorm writes every task number as #N.
func dfNorm(report string) string { return dfTaskNumber.ReplaceAllString(report, "task #N") }

// dfBlocks splits a report into its reasons, sorted.
func dfBlocks(report string) []string {
	var blocks []string
	for _, ln := range strings.SplitAfter(report, "\n") {
		if ln == "" {
			continue
		}
		if len(blocks) == 0 || !strings.HasPrefix(ln, "\t") {
			blocks = append(blocks, "")
		}
		blocks[len(blocks)-1] += ln
	}
	slices.Sort(blocks)
	return blocks
}

// dfChains is each defect's chain of task numbers, failing task first.
func dfChains(report string) [][]int {
	var chains [][]int
	for _, ln := range strings.Split(report, "\n") {
		if strings.HasPrefix(ln, "panic:") {
			chains = append(chains, nil)
		}
		if m := regexp.MustCompile(`^\ttask #(\d+) `).FindStringSubmatch(ln); m != nil && len(chains) > 0 {
			n, _ := strconv.Atoi(m[1])
			chains[len(chains)-1] = append(chains[len(chains)-1], n)
		}
	}
	return chains
}

// dfResult is one case's native outcome.
type dfResult struct {
	Name, Report, Traced string
	Status               int
	Holds                bool
}

// dfNative runs every case against the emitted program and a copy of the
// runtime with one test seam, v1's DefectRun, which runs a body as Main
// does and returns what Main writes. The emitted code is not changed.
func dfNative(t *testing.T, o *lowered, cases []*dfCase) map[string]dfResult {
	t.Helper()
	fastTierGuard(t, "a go command")
	dir := t.TempDir()
	write := func(path string, data []byte) {
		t.Helper()
		path = filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", []byte(goMod))
	write("main.go", []byte(o.mainGo()))
	for _, f := range Runtime() {
		write(f.Path, f.Data)
	}
	write("ergo/runtime/v1/defect_seam.go", []byte(dfSeam))
	var table strings.Builder
	for _, c := range cases {
		fmt.Fprintf(&table, "\t{%q, func(env *Env) error { _, err := %s(env); return err }},\n", c.name(), c.name())
	}
	write("defect_test.go", []byte(strings.Replace(dfNativeTest, "CASES", table.String(), 1)))
	out := filepath.Join(dir, "results.json")
	if output, err := nativeGoTest(t, dir, append(os.Environ(), "GOWORK=off", "GOPROXY=off", "DEFECT_OUT="+out)); err != nil {
		t.Fatalf("native defect cases: %v\n%s", err, firstLines(string(output), 120))
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var results []dfResult
	if err := json.Unmarshal(data, &results); err != nil {
		t.Fatal(err)
	}
	byName := map[string]dfResult{}
	for _, r := range results {
		byName[r.Name] = r
	}
	return byName
}

const dfSeam = `package v1

import (
	"strings"

	"ergo.local/prototype/runtime/rt"
)

type defectSink struct{}

func (defectSink) Event(rt.Event) {}

// DefectRun runs body as Main does, on an untraced or a traced root, and
// returns what Main would write, its status and the exit.
func DefectRun(traced bool, body func(*rt.Task) error) (string, int, error) {
	root := rt.Root("main")
	if traced {
		root = rt.TracedRoot("main", defectSink{})
	}
	err := runOn(root, body)
	var b strings.Builder
	status := report(&b, err)
	return b.String(), status, err
}
`

const dfNativeTest = `package main

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"

	rt "ergo.local/prototype/runtime/rt"
	ergo "ergo.local/prototype/runtime/v1"
)

type result struct {
	Name, Report, Traced string
	Status               int
	Holds                bool
}

// holds reports whether v reaches a task, or a value it cannot look into.
func holds(v reflect.Value, seen map[uintptr]bool) bool {
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			return false
		}
		if v.Type() == reflect.TypeFor[*rt.Task]() {
			return true
		}
		if seen[v.Pointer()] {
			return false
		}
		seen[v.Pointer()] = true
		return holds(v.Elem(), seen)
	case reflect.Interface:
		return !v.IsNil() && holds(v.Elem(), seen)
	case reflect.Struct:
		for i := range v.NumField() {
			if holds(v.Field(i), seen) {
				return true
			}
		}
	case reflect.Slice, reflect.Array:
		for i := range v.Len() {
			if holds(v.Index(i), seen) {
				return true
			}
		}
	case reflect.Map:
		it := v.MapRange()
		for it.Next() {
			if holds(it.Key(), seen) || holds(it.Value(), seen) {
				return true
			}
		}
	case reflect.Func, reflect.Chan, reflect.UnsafePointer:
		return !v.IsNil()
	}
	return false
}

var cases = []struct {
	name string
	run  func(*Env) error
}{
CASES}

func TestDefects(t *testing.T) {
	var out []result
	for _, c := range cases {
		body := func(task *rt.Task) error { return c.run(&Env{task: task}) }
		report, status, err := ergo.DefectRun(false, body)
		traced, _, _ := ergo.DefectRun(true, body)
		out = append(out, result{Name: c.name, Report: report, Traced: traced, Status: status, Holds: holds(reflect.ValueOf(&err), map[uintptr]bool{})})
	}
	data, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(os.Getenv("DEFECT_OUT"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}
`

// TestRuntime199DefectOwner: one owner keeps a defect's record and one
// writes it (runtime.md, G2). In runtime/v1, defectOf alone makes a Panic
// and takes the Go stack; every recover hands its panic to capture, the
// path to defectOf, in the same function; capture alone adds a defect to a
// cause; report alone calls reportDefect, which alone writes "panic: ", and
// Main alone calls report. The lowering's sources emit none of these, so no
// generated program builds or writes a report of its own.
func TestRuntime199DefectOwner(t *testing.T) {
	type use struct{ what, in string }
	var uses []use
	dir := filepath.Join("..", "..", "..", "runtime", "v1")
	files, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if !strings.HasSuffix(file.Name(), ".go") || strings.HasSuffix(file.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), filepath.Join(dir, file.Name()), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range f.Decls {
			owner := file.Name() + ":package"
			if fn, ok := decl.(*ast.FuncDecl); ok {
				owner = fn.Name.Name
			}
			// body visits one function's own body, not the literals in it:
			// each literal is visited as a function of its own.
			var body func(in string, n ast.Node)
			body = func(in string, n ast.Node) {
				recovers, captures := false, false
				ast.Inspect(n, func(x ast.Node) bool {
					switch x := x.(type) {
					case *ast.FuncLit:
						if x != n {
							body(in+".func", x.Body)
							return false
						}
					case *ast.CompositeLit:
						if id, ok := x.Type.(*ast.Ident); ok && id.Name == "Panic" {
							uses = append(uses, use{"Panic literal", in})
						}
					case *ast.BasicLit:
						if strings.Contains(x.Value, "panic: ") {
							uses = append(uses, use{`"panic: "`, in})
						}
					case *ast.CallExpr:
						switch fun := x.Fun.(type) {
						case *ast.SelectorExpr:
							switch fun.Sel.Name {
							case "Stack":
								uses = append(uses, use{"debug.Stack", in})
							case "AddDefect":
								uses = append(uses, use{"AddDefect", in})
							}
						case *ast.Ident:
							switch fun.Name {
							case "recover":
								recovers = true
							case "capture":
								captures = true
							case "reportDefect", "report":
								uses = append(uses, use{fun.Name, in})
							}
						}
					}
					return true
				})
				if recovers && !captures {
					uses = append(uses, use{"recover without capture", in})
				}
			}
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Body != nil {
				body(owner, fn.Body)
			} else {
				body(owner, decl)
			}
		}
	}
	allowed := map[use]bool{
		{"Panic literal", "defectOf"}: true,
		{"debug.Stack", "defectOf"}:   true,
		{"AddDefect", "capture"}:      true,
		{"reportDefect", "report"}:    true,
		{`"panic: "`, "reportDefect"}: true,
		{"report", "Main"}:            true,
		// LogFailures writes a member's exit as main's exit report does.
		{"report", "end"}: true,
	}
	seen := map[use]bool{}
	for _, u := range uses {
		seen[u] = true
		if !allowed[u] {
			t.Errorf("runtime/v1: %s in %s, outside the defect owner", u.what, u.in)
		}
	}
	for u := range allowed {
		if !seen[u] {
			t.Errorf("runtime/v1: no %s in %s; the scan no longer sees the owner", u.what, u.in)
		}
	}
	i1LowerSource(t, func(owner string, node ast.Node) {
		ast.Inspect(node, func(x ast.Node) bool {
			if lit, ok := x.(*ast.BasicLit); ok && lit.Kind == token.STRING {
				for _, s := range []string{"Panic{", "debug.Stack", "AddDefect", "reportDefect", "panic: "} {
					if strings.Contains(lit.Value, s) {
						t.Errorf("lowering %s emits %q: a report path outside the runtime's owner", owner, s)
					}
				}
			}
			return true
		})
	})
}
