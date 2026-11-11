package lower

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
	v1 "ergo.local/prototype/runtime/v1"
)

// TestRuntime199MaskProperty drives every release route of the release
// owner (release.go) through the emitted Go and the copied runtime. The
// release axis comes from the owner's catalog (runtimeBindings release
// routes); the dispatch axis comes from the checker's call kinds, and each
// program's Close sites must be the classes the native test drives. The
// other axes are the spec's: exit success, typed failure, defect and stop;
// stop before the call, at release entry and during the release; nested
// mask depth; a release defect with another cause; and the unmasked twins.
// Test-only seams sit at the private v1 release boundary, at Sleep and at
// Queue.End; a Virtual clock replaces the live clock of NewLiveScheduler.
// No settling sleep is needed.
// taskReleaseLaws are the caller-release bindings of a running task's
// release (#197 L8 B3t): Close masks on the caller and stops and drains the
// task, as Stop does. They release no checked Close body, so this property
// does not drive them; the runtime/v1 tests named here are their mask, stop
// and drain laws.
var taskReleaseLaws = map[string]string{
	"Task.Close": "runtime/v1 TestUnconsumedFailureDropped, TestAbandonedChild, TestTaskReturn",
	"Set.Close":  "runtime/v1 TestSetModel",
	// A Running's release (std exec, CU4) ends its program and waits,
	// masked, for its end and its group's (Release, resources R9).
	"Running.Close": "runtime/v1 TestRunningMatchesModel, TestExecStreams",
	"Running.Stop":  "runtime/v1 TestRunningMatchesModel; lower TestExecStartRuns (post-stop status)",
}

func TestRuntime199MaskProperty(t *testing.T) {
	needsBuild(t)
	routes := map[releaseRoute][]string{}
	for name, binding := range runtimeBindings {
		if binding.release == notRelease || taskReleaseLaws[name] != "" {
			continue
		}
		if _, err := v1.BodyOf(name); err != nil {
			t.Fatalf("release route %s has no runtime/v1 body: %v", name, err)
		}
		routes[binding.release] = append(routes[binding.release], name)
	}
	for route, names := range routes {
		switch route {
		case callerRelease, frameRelease:
			if len(names) != 1 {
				t.Fatalf("release route %d has bindings %v; each new binding needs its native source", route, names)
			}
		default:
			t.Fatalf("new release route %d (%v) needs its native source", route, names)
		}
	}
	if len(routes[callerRelease]) == 0 || len(routes[frameRelease]) == 0 {
		t.Fatalf("the release catalog lacks the written Close or the exit-action route: %v", routes)
	}
	want := closeDispatch(t)
	drives := map[string]bool{}
	for _, p := range maskPrograms {
		t.Run(p.name, func(t *testing.T) {
			o := mustLower(t, "mask_"+p.name, p.src())
			found := closeSites(t, o)
			for _, path := range p.paths {
				if !found[path.name] {
					t.Errorf("path %s drives no Close site of its class; the program has %v", path.name, found)
				}
				drives[path.name] = true
			}
			for class := range found {
				if !want[class] {
					t.Errorf("Close site class %s is not a dispatch path of the checker's call kinds", class)
				}
			}
			maskNative(t, p, o)
		})
	}
	for class := range want {
		if !drives[class] {
			t.Errorf("dispatch path %s has no native program", class)
		}
	}
}

// closeDispatch derives the dispatch paths that can reach a checked Close
// from the checker's call kinds. A new call kind fails until it is placed.
func closeDispatch(t *testing.T) map[string]bool {
	t.Helper()
	paths := map[string]bool{}
	for k := check.CallKind(0); k < check.CallKindCount; k++ {
		switch k {
		case check.CallFunc:
			paths["dot"], paths["plain"] = true, true
		case check.CallOp:
			paths["op"] = true
		case check.CallProviderOp:
			// A capability's Close is refused since runtime-199 stage 3b:
			// the with that built the provider releases it (resources.md
			// R24; check TestContextOwnershipRefusals), and the with's release
			// is a plain call of the checked Close (providerRelease).
		case check.CallFuncValue:
			// The value is a checked Close function: an operation's method
			// value of Close is refused (resources R33: a function value owns
			// no resource; check TestCloseMethodValueRefused).
			paths["value"] = true
		case check.CallConversion, check.CallLen, check.CallWith, check.CallPrelude, check.CallGoError, check.CallEncode, check.CallDecode:
			// A conversion, len, go.Is and go.As, encode and decode (L11)
			// and a with block call no Close; a prelude dot call is a
			// CallFunc since check-v1 stage D3a.
		case check.CallLawOp:
			// Never lowered: laws are not run (#162). A law's operation
			// call has no receiver and carries no Close fact (check
			// close.go).
		default:
			t.Fatalf("new call kind %d needs its Close dispatch path", k)
		}
	}
	return paths
}

// closeSites classifies the checker's Close facts in o by dispatch path.
func closeSites(t *testing.T, o *lowered) map[string]bool {
	t.Helper()
	info := o.pkg.Info
	callee := map[syntax.Node]bool{}
	for x := range info.Calls {
		fun := x.Fun
		for {
			if p, ok := fun.(*syntax.ParenExpr); ok {
				fun = p.X
				continue
			}
			if ix, ok := fun.(*syntax.IndexExpr); ok {
				fun = ix.X
				continue
			}
			break
		}
		callee[fun] = true
		if sel, ok := fun.(*syntax.SelectorExpr); ok {
			callee[sel.Sel] = true
		}
	}
	// A selector's own name is the selector's site, not a function value.
	for site := range info.Closes {
		if sel, ok := site.(*syntax.SelectorExpr); ok {
			callee[sel.Sel] = true
		}
	}
	found := map[string]bool{}
	for site := range info.Closes {
		switch x := site.(type) {
		case *syntax.FuncDecl:
		case *syntax.CallExpr:
			call := info.Calls[x]
			switch {
			case call == nil:
				t.Errorf("Close call %v has no resolved call", x)
			case call.Kind == check.CallFunc && call.Recv != nil:
				found["dot"] = true
			case call.Kind == check.CallFunc:
				found["plain"] = true
			case call.Kind == check.CallOp && !programReceiver(o, info.Closes[site].Receiver):
				// A std operation's Close (the paths' signal.Close on a
				// queue.Queue) is no checked Close of the program: counting
				// it would let the queue alone satisfy the op path.
			case call.Kind == check.CallOp:
				found["op"] = true
			default:
				t.Errorf("Close call of kind %d is unclassified", call.Kind)
			}
		case *syntax.Ident:
			if !callee[x] {
				found["value"] = true
			}
		case *syntax.SelectorExpr:
			if !callee[x] {
				found["methodValue"] = true
			}
		default:
			t.Errorf("unclassified Close site %T", site)
		}
	}
	return found
}

// programReceiver reports a Close receiver whose type the program declares.
func programReceiver(o *lowered, recv check.Type) bool {
	if p, ok := recv.(*check.Pointer); ok {
		recv = p.Elem
	}
	n, ok := recv.(*check.Named)
	return ok && n.Obj.Pkg != nil && n.Obj.Pkg.Path == o.pkg.Path
}

type maskPath struct {
	name, fn string
	sleep    time.Duration // the Close body's first wait
}

type maskProgram struct {
	name   string
	extra  string
	paths  []maskPath
	twins  []string
	frames bool
}

func (p maskProgram) src() string { return maskShared + p.extra }

// Each Close body waits once, then raises a defect when its receiver says
// so. Its first capability call is the Close body's first checkpoint. The
// path functions close signal immediately before the dispatch, the seam for
// a stop that is pending before the Close call.
const maskShared = `import queue
import scope
import task

type Fault {}
type Conn { Fail bool }
// Mark is Conn without a Close: it crosses (resources R1), so the twin
// owns nothing and enters no release.
type Mark { Fail bool }

func Close(c Conn) needs Scheduler {
	Scheduler.Sleep(1h)
	if c.Fail {
		let xs = []int{}
		let _ = xs[0]
	}
}

func Finish(c Mark) needs Scheduler {
	Scheduler.Sleep(1h)
	if c.Fail {
		let xs = []int{}
		let _ = xs[0]
	}
}

func viaDot(fail bool, signal queue.Queue[int]) {
	with context { Scheduler: NewLiveScheduler() } {
		let c = Conn{Fail: fail}
		signal.End()
		c.Close()
	}
}

func twinFinish(fail bool, signal queue.Queue[int]) {
	with context { Scheduler: NewLiveScheduler() } {
		let c = Mark{Fail: fail}
		signal.End()
		c.Finish()
	}
}

func fail(exit int) fails Fault needs Scheduler {
	if exit == 1 {
		return Fault
	}
	if exit == 2 {
		let xs = []int{}
		let _ = xs[0]
	}
	if exit == 3 {
		Scheduler.Sleep(7h)
	}
}

func frame(exit int, nested bool, releaseFail bool) fails Fault {
	with context { Scheduler: NewLiveScheduler() } {
		scope.OnExit() {
			Scheduler.Sleep(6h)
		}
		scope.OnExit() {
			if nested {
				Conn{Fail: releaseFail}.Close()
				Scheduler.Sleep(8h)
			} else {
				Scheduler.Sleep(5h)
				if releaseFail {
					let xs = []int{}
					let _ = xs[0]
				}
			}
		}
		try fail(exit)
	}
}

func launch() Task[int, Fault] {
	task.Go() {
		try frame(0, true, false)
		0
	}
}

func main() {}
`

// maskPrograms: a plain call and a declared function value need a program
// with one function named Close (S108).
var maskPrograms = []maskProgram{
	{
		name: "operations",
		extra: `
type Plain { Fail bool, Live LiveScheduler }
type Other { Fail bool }
type Releaser { Close() }

func Close(p Plain) {
	p.Live.Sleep(2h)
	if p.Fail {
		let xs = []int{}
		let _ = xs[0]
	}
}

func Close(o Other, n int) needs Scheduler {
	Scheduler.Sleep(1h)
	if o.Fail {
		let xs = []int{}
		let _ = xs[n]
	}
}

func viaOp(fail bool, signal queue.Queue[int]) {
	let r Releaser = Plain{Fail: fail, Live: NewLiveScheduler()}
	signal.End()
	r.Close()
}

func twinOperand(fail bool, signal queue.Queue[int]) {
	with context { Scheduler: NewLiveScheduler() } {
		let o = Other{Fail: fail}
		signal.End()
		o.Close(0)
	}
}
`,
		paths: []maskPath{
			{"dot", "viaDot", time.Hour},
			{"op", "viaOp", 2 * time.Hour},
		},
		twins:  []string{"twinFinish", "twinOperand"},
		frames: true,
	},
	{
		name: "single",
		extra: `
func viaPlain(fail bool, signal queue.Queue[int]) {
	with context { Scheduler: NewLiveScheduler() } {
		let c = Conn{Fail: fail}
		signal.End()
		Close(c)
	}
}

func viaValue(fail bool, signal queue.Queue[int]) {
	with context { Scheduler: NewLiveScheduler() } {
		let c = Conn{Fail: fail}
		let f = Close
		signal.End()
		f(c)
	}
}
`,
		paths: []maskPath{
			{"plain", "viaPlain", time.Hour},
			{"value", "viaValue", time.Hour},
		},
		twins: []string{"twinFinish"},
	},
	{
		// A generic receiver: the Close is instantiated at the call.
		name: "generic",
		extra: `
type Box[T] { Fail bool, V T }

func Close[T](b Box[T]) needs Scheduler {
	Scheduler.Sleep(3h)
	if b.Fail {
		let xs = []int{}
		let _ = xs[0]
	}
}

func viaGeneric(fail bool, signal queue.Queue[int]) {
	with context { Scheduler: NewLiveScheduler() } {
		let b = Box[string]{Fail: fail, V: "x"}
		signal.End()
		b.Close()
	}
}
`,
		paths: []maskPath{{"dot", "viaGeneric", 3 * time.Hour}},
		twins: []string{"twinFinish"},
	},
	{
		// A pointer receiver, called through a pointer value.
		name: "pointer",
		extra: `
type Held { Fail bool }

func Close(h *Held) needs Scheduler {
	Scheduler.Sleep(4h)
	if h.Fail {
		let xs = []int{}
		let _ = xs[0]
	}
}

func viaPointer(fail bool, signal queue.Queue[int]) {
	with context { Scheduler: NewLiveScheduler() } {
		let mut h = Held{Fail: fail}
		let p = &h
		signal.End()
		p.Close()
	}
}
`,
		paths: []maskPath{{"dot", "viaPointer", 4 * time.Hour}},
		twins: []string{"twinFinish"},
	},
}

func maskNative(t *testing.T, p maskProgram, o *lowered) {
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
	// Every seam is required exactly once, so drift fails rather than
	// running without a witness.
	replace := func(data []byte, from, to string) []byte {
		t.Helper()
		if n := strings.Count(string(data), from); n != 1 {
			t.Fatalf("mask seam %q occurs %d times, want 1", from, n)
		}
		return []byte(strings.Replace(string(data), from, to, 1))
	}
	for _, f := range Runtime() {
		data := f.Data
		switch f.Path {
		case "ergo/runtime/v1/runtime.go":
			from := "func Sleep(t *rt.Task, d time.Duration) {"
			data = replace(data, from, from+"\n\tMaskEnter(\"Sleep\", t, d)")
			data = replace(data, "if err := rt.Sleep(t, rt.Live{}, d); err != nil {", "err := rt.Sleep(t, rt.Live{}, d)\n\tMaskSlept(t, d, err)\n\tif err != nil {")
		case "ergo/runtime/v1/task.go":
			from := "func release(t *rt.Task, c *rt.Cause, body func(), after func(error) error) {"
			data = replace(data, from, from+"\n\tMaskEnter(\"release\", t, 0)")
		case "ergo/runtime/v1/queue.go":
			from := "func (q *Queue[T]) End() {"
			data = replace(data, from, from+"\n\tMaskEnter(\"Queue.End\", nil, 0)")
		case "ergo/runtime/rt/clock.go":
			data = replace(data, "return liveTimer{time.NewTimer(d)}", "return BlockingClock.NewTimer(d)")
		}
		write(f.Path, data)
	}
	write("ergo/runtime/rt/mask_probe.go", []byte("package rt\nvar BlockingClock *Virtual\nfunc MaskDepth(t *Task) int { return t.regions.n }\n"))
	write("ergo/runtime/v1/mask_probe.go", []byte(maskProbe))
	var paths, twins strings.Builder
	for _, path := range p.paths {
		paths.WriteString("\t\"" + path.name + "\": {" + path.fn + ", " + durationLit(path.sleep) + "},\n")
	}
	for _, twin := range p.twins {
		twins.WriteString("\t\"" + twin + "\": " + twin + ",\n")
	}
	frames := "false"
	if p.frames {
		frames = "true"
	}
	native := strings.NewReplacer("PATHS", paths.String(), "TWINS", twins.String(), "FRAMES", frames).Replace(maskNativeTest)
	write("mask_test.go", []byte(native))
	output, err := nativeGoTest(t, dir, nil, "-test.v")
	if err != nil {
		t.Fatalf("native %s: %v\n%s\nGo:\n%s", p.name, err, output, o.mainGo())
	}
	passed := strings.Count(string(output), "--- PASS: ")
	if passed == 0 {
		t.Fatalf("native %s ran no case:\n%s", p.name, output)
	}
	t.Logf("%s: %d native cases PASS (paths %v, twins %v, exit actions %v)", p.name, passed, p.paths, p.twins, p.frames)
}

func durationLit(d time.Duration) string { return fmt.Sprintf("time.Duration(%d)", int64(d)) }

const maskProbe = `package v1
import (
 "time"
 "ergo.local/prototype/runtime/rt"
)
var MaskHook func(string, *rt.Task, time.Duration)
var SleptHook func(*rt.Task, time.Duration, error)
func MaskEnter(name string, t *rt.Task, d time.Duration) { if MaskHook != nil { MaskHook(name, t, d) } }
func MaskSlept(t *rt.Task, d time.Duration, err error) { if SleptHook != nil { SleptHook(t, d, err) } }
// MaskExit unpacks a recovered panic into the exit it carries, nil for a raw panic.
func MaskExit(p any) error {
 switch e := p.(type) {
 case *exit: return e.err
 case error: if e == rt.Interrupted { return e }
 }
 return nil
}
`

const maskNativeTest = `package main
import (
 "fmt"
 "runtime"
 "sort"
 "strings"
 "sync"
 "testing"
 "time"
 rt "ergo.local/prototype/runtime/rt"
 ergo "ergo.local/prototype/runtime/v1"
)
type path struct { fn func(*Env, bool, *ergo.Queue[int]); sleep time.Duration }
var paths = map[string]path{
PATHS}
var twins = map[string]func(*Env, bool, *ergo.Queue[int]){
TWINS}
const frames = FRAMES
type slept struct { d time.Duration; task *rt.Task; depth int; done bool; err error }
type entered struct { task *rt.Task; depth int }
type trace struct { mu sync.Mutex; sleeps []*slept; releases []entered; stopped bool }
// stop is when the worker's stop is requested: "" never, "before-call" at
// Queue.End just before the dispatch, "release-entry" at the first entry
// into the v1 release boundary (before its mask), "during" at the entry of
// the wait of duration at.
type plan struct { stop string; at time.Duration }
func (s *slept) String() string {
 status := "running"
 if s.done { status = "ok"; if s.err != nil { status = s.err.Error() } }
 return fmt.Sprintf("%dh@%d:%s", int(s.d/time.Hour), s.depth, status)
}
func observe(t *testing.T, worker *rt.Task, p plan, call func() error) (*trace, string) {
 t.Helper()
 tr := &trace{}
 clock := rt.NewVirtual(time.Unix(0, 0)); rt.BlockingClock = clock
 stop := func() { if !tr.stopped { tr.stopped = true; worker.Interrupt() } }
 ergo.MaskHook = func(name string, task *rt.Task, d time.Duration) {
  tr.mu.Lock(); defer tr.mu.Unlock()
  switch name {
  case "release":
   tr.releases = append(tr.releases, entered{task, rt.MaskDepth(task)})
   if p.stop == "release-entry" { stop() }
  case "Sleep":
   tr.sleeps = append(tr.sleeps, &slept{d: d, task: task, depth: rt.MaskDepth(task)})
   if p.stop == "during" && d == p.at { stop() }
  case "Queue.End":
   if p.stop == "before-call" { stop() }
  }
 }
 ergo.SleptHook = func(task *rt.Task, d time.Duration, err error) {
  tr.mu.Lock(); defer tr.mu.Unlock()
  for i := len(tr.sleeps) - 1; i >= 0; i-- {
   if s := tr.sleeps[i]; s.d == d && s.task == task && !s.done { s.done, s.err = true, err; return }
  }
  panic("a wait completed that never entered")
 }
 // A timer exists only for a wait that will not be interrupted: a stop
 // is requested at a wait's entry, before rt.Sleep's own check, so firing
 // every pending timer cannot race a stop.
 quit, pumped := make(chan struct{}), make(chan struct{})
 go func() {
  defer close(pumped)
  for { select { case <-quit: return; default: }; if clock.Pending() > 0 { clock.Advance(24 * time.Hour) }; runtime.Gosched() }
 }()
 type result struct { err error; p any }
 done := make(chan result, 1)
 go func() {
  var r result
  func() { defer func() { r.p = recover() }(); r.err = call() }()
  done <- r
 }()
 // The call's end is an event: a call that never ends hangs here until
 // this binary's -test.timeout, whose panic names the case (fu-time-limits).
 r := <-done
 close(quit); <-pumped
 ergo.MaskHook, ergo.SleptHook = nil, nil
 if clock.Pending() != 0 { t.Fatalf("%d retained timers", clock.Pending()) }
 return tr, kinds(r.err, r.p)
}
// kinds lists the exit's reasons in order.
func kinds(err error, p any) string {
 var c rt.Cause
 c.Add(err)
 if p != nil {
  e := ergo.MaskExit(p)
  if e == nil { return fmt.Sprintf("raw panic %v", p) }
  c.Add(e)
 }
 var ks []string
 for _, r := range c.Reasons {
  switch r.Kind {
  case rt.KindFailure: ks = append(ks, "failure")
  case rt.KindDefect: if r.InRelease { ks = append(ks, "release-defect") } else { ks = append(ks, "defect") }
  case rt.KindInterruption: ks = append(ks, "interruption")
  }
 }
 return strings.Join(ks, ",")
}
func sorted[T any](m map[string]T) []string {
 var names []string
 for name := range m { names = append(names, name) }
 sort.Strings(names); return names
}
// afterStop checks the worker after the call: the mask is restored to
// depth 0 and a stop that a mask held stays pending for the next checkpoint.
func afterStop(t *testing.T, worker *rt.Task, requested bool) {
 t.Helper()
 if d := rt.MaskDepth(worker); d != 0 { t.Fatalf("mask depth %d leaked into the caller", d) }
 if worker.StopRequested() != requested { t.Fatalf("stop requested %v, want %v", worker.StopRequested(), requested) }
 observed := func() (p any) { defer func() { p = recover() }(); worker.Check(); return nil }()
 if requested && observed != rt.Interrupted { t.Fatalf("the next checkpoint did not observe the pending stop: %v", observed) }
}
// Every dispatch path reaches the masked Close body: the body runs once on
// the calling task, inside one release, and finishes its wait whatever stop
// arrives, before the call, at release entry or during the wait. Its defect
// leaves as a release defect. The release restores the caller's mask depth,
// 0 or an outer 1, on the normal and the defect path.
func TestClosePaths(t *testing.T) {
 for _, name := range sorted(paths) {
  path := paths[name]
  for _, outer := range []int{0, 1} {
   for _, fail := range []bool{false, true} {
    for _, stop := range []string{"", "before-call", "release-entry", "during"} {
     t.Run(fmt.Sprintf("%s/outer%d/fail=%v/stop=%s", name, outer, fail, stop), func(t *testing.T) {
      worker := rt.Root("worker")
      signal := ergo.New[int](0)
      after := -1
      tr, got := observe(t, worker, plan{stop, path.sleep}, func() error {
       if outer == 1 { m := worker.Mask(); defer m.Unmask() }
       defer func() { after = rt.MaskDepth(worker) }()
       path.fn(&Env{task: worker}, fail, signal)
       return nil
      })
      want := ""
      if fail { want = "release-defect" }
      if got != want { t.Fatalf("exit %q, want %q", got, want) }
      if len(tr.releases) != 1 { t.Fatalf("%d release entries, want 1", len(tr.releases)) }
      if e := tr.releases[0]; e.task != worker || e.depth != outer { t.Fatalf("release entered on the wrong task or at depth %d, want %d", e.depth, outer) }
      if len(tr.sleeps) != 1 { t.Fatalf("waits %v, want one", tr.sleeps) }
      s := tr.sleeps[0]
      if s.task != worker { t.Fatal("the Close body waited on another task") }
      if wantSleep := fmt.Sprintf("%dh@%d:ok", int(path.sleep/time.Hour), outer+1); s.String() != wantSleep { t.Fatalf("wait %v, want %s", s, wantSleep) }
      if after != outer { t.Fatalf("mask depth after Close %d, want %d", after, outer) }
      afterStop(t, worker, stop != "")
     })
    }
   }
  }
 }
}
// The unmasked twins, a same-shaped method with another name and a Close
// with an operand, enter no release and are interrupted.
func TestTwins(t *testing.T) {
 for _, name := range sorted(twins) {
  for _, stop := range []string{"", "before-call", "during"} {
   t.Run(name+"/stop="+stop, func(t *testing.T) {
    worker := rt.Root("worker")
    tr, got := observe(t, worker, plan{stop, time.Hour}, func() error { twins[name](&Env{task: worker}, false, ergo.New[int](0)); return nil })
    if len(tr.releases) != 0 { t.Fatalf("an unmasked twin entered %d releases", len(tr.releases)) }
    want, waits := "", fmt.Sprint([]string{"1h@0:ok"})
    switch stop {
    case "before-call": want, waits = "interruption", fmt.Sprint([]string{})
    case "during": want, waits = "interruption", fmt.Sprint([]string{"1h@0:interrupted"})
    }
    var actual []string
    for _, s := range tr.sleeps { actual = append(actual, s.String()) }
    if got != want || fmt.Sprint(actual) != waits { t.Fatalf("exit %q waits %v, want %q %s", got, actual, want, waits) }
    afterStop(t, worker, stop != "")
   })
  }
 }
}
// frameKinds is the frame's exit: its body's exit first, then each release
// defect, then a stop requested during the releases, which the frame's end
// observes once the releases have unmasked (Exit causes 4 and 5).
func frameKinds(exit int, stop string, releaseFail bool) string {
 var ks []string
 switch exit {
 case 1: ks = append(ks, "failure")
 case 2: ks = append(ks, "defect")
 case 3: ks = append(ks, "interruption")
 }
 if releaseFail { ks = append(ks, "release-defect") }
 if stop != "" { ks = append(ks, "interruption") }
 return strings.Join(ks, ",")
}
// Exit actions run masked, last registered first, once each, on the frame's
// task, for every frame exit. A nested Close in an exit action masks to
// depth 2 and restores depth 1. A release defect joins the frame's earlier
// exit and the later exit action still runs.
func TestExitActions(t *testing.T) {
 if !frames { t.Skip("exit actions run in the operations program") }
 for _, nested := range []bool{false, true} {
  for _, exit := range []int{0, 1, 2, 3} {
   stops := []string{"", "release-entry", "during"}
   if exit == 3 { stops = []string{""} }
   for _, stop := range stops {
    for _, releaseFail := range []bool{false, true} {
     t.Run(fmt.Sprintf("nested=%v/exit%d/stop=%s/releaseFail=%v", nested, exit, stop, releaseFail), func(t *testing.T) {
      worker := rt.Root("worker")
      p := plan{stop, 5 * time.Hour}
      if nested { p.at = time.Hour }
      if exit == 3 { p = plan{"during", 7 * time.Hour} }
      tr, got := observe(t, worker, p, func() error { return frame(&Env{task: worker}, exit, nested, releaseFail) })
      var waits []string
      if exit == 3 { waits = append(waits, "7h@0:interrupted") }
      if nested {
       waits = append(waits, "1h@2:ok")
       if !releaseFail { waits = append(waits, "8h@1:ok") }
      } else {
       waits = append(waits, "5h@1:ok")
      }
      waits = append(waits, "6h@1:ok")
      var actual []string
      for _, s := range tr.sleeps {
       actual = append(actual, s.String())
       if s.task != worker { t.Fatal("an exit action waited on another task") }
      }
      if fmt.Sprint(actual) != fmt.Sprint(waits) { t.Fatalf("waits %v, want %v", actual, waits) }
      depths := []int{0, 0}
      if nested { depths = []int{0, 1, 0} }
      var entries []int
      for _, e := range tr.releases {
       entries = append(entries, e.depth)
       if e.task != worker { t.Fatal("a release entered on another task") }
      }
      if fmt.Sprint(entries) != fmt.Sprint(depths) { t.Fatalf("release entry depths %v, want %v", entries, depths) }
      if want := frameKinds(exit, stop, releaseFail); got != want { t.Fatalf("exit %q, want %q", got, want) }
      afterStop(t, worker, stop != "" || exit == 3)
     })
    }
   }
  }
 }
}
// The exit action of a frame in a started task runs on that task, not on
// the task that started it: every release entry and every wait in the
// emitted callbacks sees one task, the child's.
func TestExitActionTask(t *testing.T) {
 if !frames { t.Skip("exit actions run in the operations program") }
 parent := rt.Root("parent")
 tr, got := observe(t, parent, plan{}, func() error {
  h := launch(&Env{task: parent})
  _, err := h.Wait()
  return err
 })
 if got != "" { t.Fatalf("exit %q", got) }
 if len(tr.releases) != 3 || len(tr.sleeps) != 3 { t.Fatalf("releases %d waits %v", len(tr.releases), tr.sleeps) }
 child := tr.releases[0].task
 if child == nil || child == parent { t.Fatal("the exit action released on the parent's task") }
 for _, e := range tr.releases { if e.task != child { t.Fatal("releases entered on different tasks") } }
 for _, s := range tr.sleeps { if s.task != child { t.Fatal("an emitted callback waited on another task than its release") } }
}
`

// TestRuntime199ExternClose: a checked Close with an extern body would
// return before closeBody and run unmasked. No source reaches it today (a
// user extern is refused at its use; only prelude externs lower), so the
// test gives a checked Close an extern target directly and expects the ICE.
func TestRuntime199ExternClose(t *testing.T) {
	const src = `type Conn { Fail bool }

func Close(c Conn) {
	if c.Fail {
		let xs = []int{}
		let _ = xs[0]
	}
}

func main() {
	Close(Conn{Fail: false})
}
`
	o := mustLower(t, "externClose", src)
	var close *check.Func
	for _, f := range o.pkg.Funcs {
		if f.Name == "Close" {
			close = f
		}
	}
	if close == nil || !isClose(close) {
		t.Fatalf("Close has no checked Close fact")
	}
	l := newLowerer(Unit{Package: o.pkg, Files: []*syntax.File{o.file}, Sources: []Source{{Path: testSourcePath, Text: src}}})
	l.nameAll()
	fd := l.funcDecl(close)
	fd.extern = runtimeModule + "/runtime/v1.Release"
	var got any
	func() {
		defer func() { got = recover() }()
		l.emitFuncBody(fd, false)
	}()
	ref, ok := got.(refusal)
	if !ok || !strings.Contains(ref.msg, "internal compiler error: the checked Close Close has an extern body, which no release route masks") {
		t.Fatalf("an extern-bodied checked Close lowered as %v, want the unmasked-Close ICE", got)
	}
}
