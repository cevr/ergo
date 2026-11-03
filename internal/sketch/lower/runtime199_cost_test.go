package lower

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The stage 5 cost receipt (runtime.md I1, #190). TestRuntime199CostModule
// writes one Go module that holds the actual emitted workload and a
// handwritten target-shape control, benchmarked in one process by
// BenchmarkRuntime199Emitted and BenchmarkRuntime199Target. Each has three
// sub-benchmarks:
//   - work=dispatch: one capability call through the env's provider;
//   - work=impure-loop: a numeric loop of costLoopN iterations in a frame
//     whose footprint is nonempty, so each back-edge polls;
//   - work=pure-loop: the same loop in a pure frame, the negative control:
//     neither side polls.
//
// The target keeps the emitted contract: the same task, an env parameter
// that carries it, the same provider interface (operations take the
// caller's task), a poll at the same sites (the capability call and each
// back-edge of an impure frame), and no cleanup work, because the emitted
// workload registers none. It leaves out only the emitted representation:
// the operations adapter, its Env and the Range value.
//
// Only the generated calls are timed. The module is built by the go command
// before the benchmark process starts; the env and its provider are built
// before the timer is reset. A counter and a package sink keep every result.
// Each sub-benchmark reports polls/op: the dynamic poll count of one op,
// counted at the poll site by a copy of the same code (package counted) in
// which every poll calls a counter. The timed code has no counter.
//
// With RUNTIME199_COST_DIR set the module is written there for
// accept/cost.sh; otherwise into a temporary directory. Either way the test
// runs the module's contract test and each benchmark once, so the gate
// proves the module builds, the target computes the emitted results, both
// poll the same number of times, a stopped task interrupts both at the same
// sites, and no sub-benchmark is empty.

// costLoopN is the loop workload size, the iterations of one loop op.
const costLoopN = 1024

// costSource is the emitted workload. Keep.Put is a pure provider method, so
// the measured cost is the dispatch and its poll, not a provider body.
const costSource = `type Sink {
	Put(v int) int,
}

type Keep {}

func Put(_ Keep, v int) int {
	v + 1
}

let Kept = context { Sink: Keep }

func dispatch(x int) int needs Sink {
	Sink.Put(x)
}

func impure(n int) int needs Sink {
	let mut sum = 0
	for i in 0..n {
		sum = sum + i
	}
	Sink.Put(sum)
}

func pure(n int) int {
	let mut sum = 0
	for i in 0..n {
		sum = sum + i
	}
	sum + 1
}

func main() {
	with Kept {
		dispatch(1)
		impure(2)
	}
	pure(3)
}
`

// costStaticPolls is the static poll count of each emitted function: the
// capability call's poll in dispatch; the back-edge and the capability call
// in impure; none in the pure frames (I1).
var costStaticPolls = map[string]int{"dispatch": 1, "impure": 2, "pure": 0, "Keep.Put": 0, "main_1": 0}

// costPoll is a poll site in Go source: a Check call on a task expression.
var costPoll = regexp.MustCompile(`\b([A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z_][A-Za-z0-9_]*)*)\.Check\(\)`)

func TestRuntime199CostModule(t *testing.T) {
	dir := os.Getenv("RUNTIME199_COST_DIR")
	if dir == "" {
		dir = t.TempDir()
	} else if !filepath.IsAbs(dir) {
		t.Fatalf("RUNTIME199_COST_DIR %q must be absolute", dir)
	}
	o := mustLower(t, "cost", costSource)
	emitted := o.mainGo()

	static := costFuncPolls(t, emitted)
	for name, want := range costStaticPolls {
		if got, ok := static[name]; !ok || got != want {
			t.Errorf("emitted %s has %d static polls (present %v), want %d", name, got, ok, want)
		}
	}
	total := 0
	for _, n := range static {
		total += n
	}
	if n := len(costPoll.FindAllString(emitted, -1)); n != total || n != strings.Count(emitted, ".Check()") {
		t.Fatalf("poll sites: regexp %d, AST %d, text %d; the counting copy would miss a site", n, total, strings.Count(emitted, ".Check()"))
	}
	t.Logf("static polls of the emitted workload: %v (total %d)", static, total)
	if tp := costFuncPolls(t, "package main\n"+costTarget); tp["targetDispatch"] != static["dispatch"] || tp["targetImpure"] != static["impure"] || tp["targetPure"] != static["pure"] {
		t.Fatalf("target static polls %v differ from the emitted %v", tp, static)
	}

	files := map[string]string{
		"go.mod":             goMod,
		"main.go":            emitted,
		"target.go":          "package main\n" + costTarget,
		"cost_test.go":       strings.Replace(costBench, "LOOPN", strconv.Itoa(costLoopN), 1),
		"counted/emitted.go": costCounted(t, emitted),
		"counted/target.go":  costCounted(t, "package main\n"+costTarget),
		"counted/count.go":   costCount,
	}
	for _, f := range Runtime() {
		files[f.Path] = string(f.Data)
	}
	costWrite(t, dir, files)

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "test", "-count=1", "-timeout=3m", "-v",
		"-run", "^TestCostContract$", "-bench", "^BenchmarkRuntime199(Emitted|Target)$", "-benchtime=1x", ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOPROXY=off", "GOFLAGS=")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("cost module: %v\n%s\nGo:\n%s", err, output, stripDirectives(emitted))
	}
	// Each sub-benchmark ran one op and reported its counted polls.
	wantPolls := map[string]string{"dispatch": "1", "impure-loop": strconv.Itoa(costLoopN + 1), "pure-loop": "0"}
	for _, side := range []string{"Emitted", "Target"} {
		for _, work := range []string{"dispatch", "impure-loop", "pure-loop"} {
			name := "BenchmarkRuntime199" + side + "/work=" + work
			m := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(name) + `(?:-\d+)?\s+1\s+\S+ ns/op\s+(\S+) polls/op`).FindSubmatch(output)
			if m == nil {
				t.Errorf("benchmark %s did not run one op with its poll count:\n%s", name, output)
				continue
			}
			if got, err := strconv.ParseFloat(string(m[1]), 64); err != nil || strconv.FormatFloat(got, 'f', -1, 64) != wantPolls[work] {
				t.Errorf("benchmark %s counted %s polls/op, want %s", name, m[1], wantPolls[work])
			}
		}
	}
	if !strings.Contains(string(output), "--- PASS: TestCostContract") {
		t.Errorf("the contract test did not pass:\n%s", output)
	}
}

// costFuncPolls counts the poll sites of each function of a Go file, by
// name (a method as Recv.Name).
func costFuncPolls(t *testing.T, src string) map[string]int {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	polls := map[string]int{}
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok {
			continue
		}
		name := fd.Name.Name
		if fd.Recv != nil && len(fd.Recv.List) == 1 {
			typ := fd.Recv.List[0].Type
			if star, ok := typ.(*ast.StarExpr); ok {
				typ = star.X
			}
			if id, ok := typ.(*ast.Ident); ok {
				name = id.Name + "." + name
			}
		}
		n := 0
		ast.Inspect(fd, func(x ast.Node) bool {
			if call, ok := x.(*ast.CallExpr); ok && len(call.Args) == 0 {
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Check" {
					n++
				}
			}
			return true
		})
		polls[name] = n
	}
	return polls
}

// costCounted is the counting copy of a main-package file: package counted,
// with every poll site calling ergoCountPoll, which counts and then polls.
func costCounted(t *testing.T, src string) string {
	t.Helper()
	if !strings.Contains(src, "package main\n") {
		t.Fatal("the source is not package main")
	}
	src = strings.Replace(src, "package main\n", "package counted\n", 1)
	out := costPoll.ReplaceAllString(src, "ergoCountPoll($1)")
	if strings.Contains(out, ".Check()") {
		t.Fatalf("a poll site escaped the counting copy:\n%s", out)
	}
	return out
}

// costWrite writes the module. An existing directory must be an earlier
// cost module: the test adds and replaces files and never deletes one, so
// a stale file fails the test rather than entering the benchmark.
func costWrite(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	if entries, err := os.ReadDir(dir); err == nil && len(entries) > 0 {
		mod, err := os.ReadFile(filepath.Join(dir, "go.mod"))
		if err != nil || !strings.HasPrefix(string(mod), "module "+programModule+"\n") {
			t.Fatalf("%s is not empty and holds no cost module", dir)
		}
	}
	for rel, data := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var stale []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		if _, ok := files[filepath.ToSlash(rel)]; !ok {
			stale = append(stale, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(stale)
	if len(stale) > 0 {
		t.Fatalf("%s holds files this generator did not write: %v", dir, stale)
	}
}

// costTarget is the handwritten target shape (#190): what a Go programmer
// writes for the same contract. The env carries the task and the provider;
// the provider interface's operation takes the caller's task; the frame
// polls before the capability call and at the end of each back-edge of the
// impure loop; the pure loop polls nowhere. It has no adapter, no per-call
// Env and no Range value.
const costTarget = `
import rt "ergo.local/prototype/runtime/rt"

type targetSink interface {
	Put(t *rt.Task, v int) int
}

type targetKeep struct{}

func (targetKeep) Put(_ *rt.Task, v int) int { return v + 1 }

type targetEnv struct {
	task *rt.Task
	Sink targetSink
}

func newTargetEnv(task *rt.Task) *targetEnv { return &targetEnv{task: task, Sink: targetKeep{}} }

func targetDispatch(env *targetEnv, x int) int {
	env.task.Check()
	return env.Sink.Put(env.task, x)
}

func targetImpure(env *targetEnv, n int) int {
	sum := 0
	for i := 0; i < n; i++ {
		sum += i
		env.task.Check()
	}
	env.task.Check()
	return env.Sink.Put(env.task, sum)
}

func targetPure(env *targetEnv, n int) int {
	sum := 0
	for i := 0; i < n; i++ {
		sum += i
	}
	return sum + 1
}
`

// costCount is the counting package's counter and entry points. Each entry
// builds its env as the benchmark does, resets the counter, runs one op and
// returns its result and the polls it made.
const costCount = `package counted

import rt "ergo.local/prototype/runtime/rt"

var polls int

func ergoCountPoll(t *rt.Task) {
	polls++
	t.Check()
}

func count(f func() int) (int, int) {
	polls = 0
	r := f()
	return r, polls
}

// Emitted runs one op of the emitted workload on task.
func Emitted(work string, task *rt.Task, x int) (int, int) {
	env := &Env{task: task}
	buildKept(env)
	switch work {
	case "dispatch":
		return count(func() int { return dispatch(env, x) })
	case "impure-loop":
		return count(func() int { return impure(env, x) })
	case "pure-loop":
		return count(func() int { return pure(env, x) })
	}
	panic("unknown work " + work)
}

// Target runs one op of the target on task.
func Target(work string, task *rt.Task, x int) (int, int) {
	env := newTargetEnv(task)
	switch work {
	case "dispatch":
		return count(func() int { return targetDispatch(env, x) })
	case "impure-loop":
		return count(func() int { return targetImpure(env, x) })
	case "pure-loop":
		return count(func() int { return targetPure(env, x) })
	}
	panic("unknown work " + work)
}
`

// costBench is the module's benchmark and contract test.
const costBench = `package main

import (
	"testing"

	"ergo.local/program/counted"
	rt "ergo.local/prototype/runtime/rt"
)

// loopN is the iterations of one loop op. A variable, so the compiler
// cannot fold the loop bound.
var loopN = LOOPN

// sink keeps every result alive.
var sink int

// emittedEnv builds the env as the emitted with does: a copy of the
// caller's env with the context's providers built into it.
func emittedEnv(task *rt.Task) *Env {
	env := &Env{task: task}
	buildKept(env)
	return env
}

// polls counts the dynamic polls of one op at the poll sites of the
// counting copy, then resets the timer. The returned report records them
// after the timed loop (ResetTimer drops metrics reported before it).
func polls(b *testing.B, side, work string, x int) (report func()) {
	run := counted.Emitted
	if side == "target" {
		run = counted.Target
	}
	_, n := run(work, rt.Root("count"), x)
	b.ResetTimer()
	return func() { b.ReportMetric(float64(n), "polls/op") }
}

func BenchmarkRuntime199Emitted(b *testing.B) {
	task := rt.Root("bench")
	env := emittedEnv(task)
	b.Run("work=dispatch", func(b *testing.B) {
		report := polls(b, "emitted", "dispatch", 0)
		s := 0
		for i := 0; i < b.N; i++ {
			s += dispatch(env, i)
		}
		sink = s
		report()
	})
	b.Run("work=impure-loop", func(b *testing.B) {
		report := polls(b, "emitted", "impure-loop", loopN)
		s := 0
		for i := 0; i < b.N; i++ {
			s += impure(env, loopN)
		}
		sink = s
		report()
	})
	b.Run("work=pure-loop", func(b *testing.B) {
		report := polls(b, "emitted", "pure-loop", loopN)
		s := 0
		for i := 0; i < b.N; i++ {
			s += pure(env, loopN)
		}
		sink = s
		report()
	})
}

func BenchmarkRuntime199Target(b *testing.B) {
	task := rt.Root("bench")
	env := newTargetEnv(task)
	b.Run("work=dispatch", func(b *testing.B) {
		report := polls(b, "target", "dispatch", 0)
		s := 0
		for i := 0; i < b.N; i++ {
			s += targetDispatch(env, i)
		}
		sink = s
		report()
	})
	b.Run("work=impure-loop", func(b *testing.B) {
		report := polls(b, "target", "impure-loop", loopN)
		s := 0
		for i := 0; i < b.N; i++ {
			s += targetImpure(env, loopN)
		}
		sink = s
		report()
	})
	b.Run("work=pure-loop", func(b *testing.B) {
		report := polls(b, "target", "pure-loop", loopN)
		s := 0
		for i := 0; i < b.N; i++ {
			s += targetPure(env, loopN)
		}
		sink = s
		report()
	})
}

// TestCostContract: the target computes the emitted results and polls as
// often at run time; a stopped task interrupts the dispatch and the impure
// loop (an empty one too, at its capability call) on both sides, and
// neither pure loop observes the stop.
func TestCostContract(t *testing.T) {
	works := []string{"dispatch", "impure-loop", "pure-loop"}
	for _, work := range works {
		for _, x := range []int{0, 1, 7, loopN} {
			er, ep := counted.Emitted(work, rt.Root("e"), x)
			tr, tp := counted.Target(work, rt.Root("t"), x)
			want := map[string]int{"dispatch": 1, "impure-loop": x + 1, "pure-loop": 0}[work]
			if er != tr || ep != tp || ep != want {
				t.Errorf("%s(%d): emitted %d with %d polls, target %d with %d polls, want equal results and %d polls", work, x, er, ep, tr, tp, want)
			}
		}
	}
	interrupted := func(f func()) (stopped bool) {
		defer func() {
			if p := recover(); p != nil {
				if p != rt.Interrupted {
					panic(p)
				}
				stopped = true
			}
		}()
		f()
		return false
	}
	stop := rt.Root("stop")
	stop.Interrupt()
	env, tenv := emittedEnv(stop), newTargetEnv(stop)
	for _, c := range []struct {
		work              string
		emitted, target   func()
		wantStopped       bool
	}{
		{"dispatch", func() { sink = dispatch(env, 1) }, func() { sink = targetDispatch(tenv, 1) }, true},
		{"impure-loop", func() { sink = impure(env, 3) }, func() { sink = targetImpure(tenv, 3) }, true},
		{"impure-loop empty", func() { sink = impure(env, 0) }, func() { sink = targetImpure(tenv, 0) }, true},
		{"pure-loop", func() { sink = pure(env, 3) }, func() { sink = targetPure(tenv, 3) }, false},
	} {
		if e, g := interrupted(c.emitted), interrupted(c.target); e != c.wantStopped || g != c.wantStopped {
			t.Errorf("%s on a stopped task: emitted interrupted %v, target %v, want %v", c.work, e, g, c.wantStopped)
		}
	}
}
`
