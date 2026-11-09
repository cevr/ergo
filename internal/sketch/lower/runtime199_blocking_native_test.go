package lower

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	v1 "ergo.local/prototype/runtime/v1"
)

// The primitive axis comes from the binding owner's production catalog.
// Each native program executes the actual emitted call and copied runtime.
// Test-only entry barriers sit inside the primitives, after source entry
// polls. They also expose queue parking and replace Live's timer with Virtual.
// No production hook, settling sleep or guessed delay is required.
func TestRuntime199BlockingProperty(t *testing.T) {
	needsBuild(t)
	var names []string
	for name, binding := range runtimeBindings {
		body, err := v1.BodyOf(name)
		if err != nil {
			t.Fatal(err)
		}
		if hasHidden(body.Hidden, v1.CallerTask) != binding.takesCaller() {
			t.Errorf("%s: hidden task and waiting policy disagree", name)
		}
		switch binding.wait {
		case noWait:
		case childWait:
			// A child join parks on no primitive of its own: its stop, mask
			// and drain laws are the runtime/v1 tests of its body, which
			// must exist (runtime/v1/timeout_test.go).
			if !childWaitLaws[name] {
				t.Errorf("%s joins children and names no runtime/v1 law test", name)
			}
		default:
			if hostWaitLaws[name] {
				// A host wait parks on a host process, not on a runtime
				// primitive the native barrier can hold; its stop, mask and
				// reap laws are its runtime/v1 model test.
				continue
			}
			names = append(names, name)
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		t.Fatal("the owner has no blocking bindings")
	}
	for _, name := range names {
		for _, route := range blockingRoutes(name, runtimeBindings[name]) {
			t.Run(name+"/"+route, func(t *testing.T) { blockingNative(t, name, route) })
		}
	}
}

// childWaitLaws are the childWait bindings whose stop, mask and drain laws
// runtime/v1/timeout_test.go states: TestAfterParentStopped (a stop of
// the caller is the interruption, both children drained),
// TestAfterMaskedCaller (a masked caller runs After to its end, the stop
// pending), TestAfterBodyFirst and TestAfterDeadlineFromScheduler (no
// child outlives the call).
var childWaitLaws = map[string]bool{"After": true}

// hostWaitLaws are the callerWait bindings that wait for a host process
// (std-align S3c): runtime/v1/exec_test.go TestExecStopModel states their
// laws (a stop before the call starts nothing, a stop while the program
// runs kills its group and returns after the reap, a masked caller runs
// it to its end), and lower TestExecRuns stops a task that waits in one.
var hostWaitLaws = map[string]bool{"ExecRun": true}

// blockingRoutes is the route axis of a binding (runtime.md, I1): the
// waiting task of a callerWait binding is the task that invokes the call,
// not the task that made the function value or the Seq it is called
// through. The maker runs in another task, so a binding that takes the
// maker's environment parks the wrong task. A Task result's route (the
// returned Task runs its own primitive) is TestReturnedTaskEnvironment,
// which every program runs.
func blockingRoutes(name string, b runtimeBinding) []string {
	routes := []string{"direct"}
	if b.wait == callerWait {
		routes = append(routes, "value")
		if body, err := v1.BodyOf(name); err == nil && defersCall(body) {
			routes = append(routes, "seq")
		}
	}
	return routes
}

// blockingNative runs one primitive on one route natively. On a value or
// seq route, maker builds the function value or the Seq in the
// controller's task, and subject calls it in the waiter's. The value is the
// operation's method value (Values': one that makes the Seq); Sleep is a
// function on its type, which has no method value (S104), so its value is a
// function literal that calls it.
func blockingNative(t *testing.T, name, route string) {
	t.Helper()
	param, arg := "q queue.Queue[int]", "q"
	var body, maker string
	child := "let s = NewLiveScheduler(); s.Sleep(1h); 7"
	switch route + " " + name {
	case "direct Sleep":
		param, body = "_ queue.Queue[int]", child
	case "direct Queue.Push":
		body = "try q.Push(7); 7"
	case "direct Queue.Pop":
		body = "try q.Pop()"
	case "direct Queue.Values":
		body = "for v in q.Values() { return v }; 0"
	case "direct Task.Wait":
		param, arg = "h Task[int, queue.Closed]", "h"
		body = "try h.Wait()"
	case "direct Task.Stop":
		param, arg = "h Task[int, queue.Closed]", "h"
		body = "h.Stop(); 0"
	case "value Sleep":
		maker = "func maker(_ queue.Queue[int]) func(duration) { let s = NewLiveScheduler(); func(d duration) { s.Sleep(d) } }"
		param, body = "f func(duration)", "f(1h); 7"
	case "value Queue.Push":
		maker = "func maker(q queue.Queue[int]) func(int) fails queue.Closed { q.Push }"
		param, body = "f func(int) fails queue.Closed", "try f(7); 7"
	case "value Queue.Pop":
		maker = "func maker(q queue.Queue[int]) func() int fails queue.Closed { q.Pop }"
		param, body = "f func() int fails queue.Closed", "try f()"
	case "value Queue.Values":
		maker = "func maker(q queue.Queue[int]) func() Seq[int] { q.Values }"
		param, body = "f func() Seq[int]", "for v in f() { return v }; 0"
	case "seq Queue.Values":
		maker = "func maker(q queue.Queue[int]) Seq[int] { q.Values() }"
		param, body = "s Seq[int]", "for v in s { return v }; 0"
	case "direct Timeline.Wait":
		// time.Manual's Sleep waits on its Timeline (std-align S2b); the
		// copied NewTimeline uses the probe's clock, so the harness parks
		// and readies it as it does Sleep's.
		param, body = "_ queue.Queue[int]", "let m = time.NewManual(0); m.Sleep(1h); 7"
	case "value Timeline.Wait":
		maker = "func maker(_ queue.Queue[int]) func(duration) { let m = time.NewManual(0); func(d duration) { m.Sleep(d) } }"
		param, body = "f func(duration)", "f(1h); 7"
	default:
		t.Fatalf("new primitive route %s %s needs its native source and readiness law", route, name)
	}
	switch {
	case strings.HasPrefix(name, "Task."):
		child = "let h = task.Go() { " + child + " }; try subject(h)"
	case maker != "":
		child, arg = "try subject(maker(q))", "maker(&Env{task:control}, q)"
	default:
		child = "try subject(q)"
	}
	imports := "import queue\nimport task\n"
	if strings.HasPrefix(name, "Timeline.") {
		imports += "import time\n"
	}
	src := imports + maker + "\nfunc subject(" + param + ") int fails queue.Closed { " + body + " }\n" +
		"func launch(q queue.Queue[int]) Task[int, queue.Closed] { task.Go() { " + child + " } }\nfunc main() {}\n"
	o := mustLower(t, "blocking", src)
	if maker != "" && !envParams(t, o.mainGo())["maker"] {
		arg = "maker(q)" // a pure maker takes no environment (pure.go, D-S5-10)
	}
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
	// Every replacement is required exactly once, so an instrumentation
	// drift fails rather than silently running without an entry witness.
	replace := func(data []byte, from, to string) []byte {
		t.Helper()
		if n := strings.Count(string(data), from); n != 1 {
			t.Fatalf("entry seam %q occurs %d times, want 1", from, n)
		}
		return []byte(strings.Replace(string(data), from, to, 1))
	}
	for _, f := range Runtime() {
		data := f.Data
		switch f.Path {
		case "ergo/runtime/v1/runtime.go":
			from := "func Sleep(t *rt.Task, d time.Duration) {"
			data = replace(data, from, from+"\n\tdefer BlockingEnter(\"Sleep\", t)()")
		case "ergo/runtime/v1/queue.go":
			for _, op := range []struct{ sig, name string }{
				{"func (q *Queue[T]) Push(t *rt.Task, v T) error {", "Queue.Push"},
				{"func (q *Queue[T]) Pop(t *rt.Task) (T, error) {", "Queue.Pop"},
				{"func (q *Queue[T]) Values(t *rt.Task) iter.Seq[T] {", "Queue.Values"},
			} {
				data = replace(data, op.sig, op.sig+"\n\tdefer BlockingEnter("+fmt.Sprintf("%q", op.name)+", t)()")
			}
		case "ergo/runtime/v1/task.go":
			for _, op := range []struct{ sig, name string }{
				{"func (t *Task[T]) Wait() (T, error) {", "Task.Wait"},
				{"func (t *Task[T]) Stop() {", "Task.Stop"},
			} {
				data = replace(data, op.sig, op.sig+"\n\tdefer BlockingEnter("+fmt.Sprintf("%q", op.name)+", BlockingOwner(t))()")
			}
		case "ergo/runtime/v1/time.go":
			from := "func (l *Timeline) Wait(t *rt.Task, d time.Duration) {"
			data = replace(data, from, from+"\n\tdefer BlockingEnter(\"Timeline.Wait\", t)()")
			data = replace(data, "return &Timeline{v: rt.NewVirtual(start), start: start}", "return &Timeline{v: rt.BlockingClock, start: start}")
		case "ergo/runtime/rt/clock.go":
			data = replace(data, "return liveTimer{time.NewTimer(d)}", "return BlockingClock.NewTimer(d)")
		case "ergo/runtime/rt/handle.go":
			data = replace(data, "close(h.done)", "ready := BlockingFinished\n\tclose(h.done)\n\tif ready != nil { ready <- &h.task }")
		}
		write(f.Path, data)
	}
	write("ergo/runtime/rt/blocking_probe.go", []byte("package rt\nvar BlockingClock *Virtual\nvar BlockingFinished chan *Task\n"))
	write("ergo/runtime/v1/blocking_probe.go", []byte(blockingProbe))
	native := strings.NewReplacer("PRIMITIVE", fmt.Sprintf("%q", name), "SUBJECT_ARG", arg).Replace(blockingNativeTest)
	write("blocking_test.go", []byte(native))
	output, err := nativeGoTest(t, dir)
	if err != nil {
		t.Fatalf("native %s: %v\n%s\nErgo:\n%s\nGo:\n%s", name, err, output, src, o.mainGo())
	}
	t.Logf("%s: stop-phase/mask cases, moved-handle cases where applicable, and returned-task environment; native PASS", name)
}

// TestRuntime199DeliveredBeforeStop is the deterministic witness of the
// stage 5a O1 rule (runtime.md, I1 rule 2): a compiler-inserted poll never
// separates the completion of a value-delivering step from the first user
// statement that receives the value. A test seam in the copied queue runs
// right after a take succeeds, on the taking task, and requests that task's
// stop: the value is delivered, then the stop is requested, before any
// statement of the body. The body must still see the value, and the stop
// must stay pending for the next poll. No race is involved.
//   - values: a Values loop's body receives the element (the back-edge poll
//     ends the iteration; at the body's top it took the value and raised
//     interruption, the darwin concurrent-ready failure);
//   - seq-over-values: a program Seq that wraps Values, so both the Seq's
//     loop and the consuming loop hand the element on before any poll;
//   - pop-loop: Pop in a loop, the value bound by the statement;
//   - pop-capability: Pop as a capability call's operand in a loop (the
//     call's poll precedes its operands, not the operation).
func TestRuntime199DeliveredBeforeStop(t *testing.T) {
	needsBuild(t)
	cases := []struct{ name, src string }{
		{"values", "func subject(q queue.Queue[int]) int fails queue.Closed { for v in q.Values() { return v }; 0 }\n"},
		{"seq-over-values", `func wrap(q queue.Queue[int]) Seq[int] {
	Seq[int](func(yield func(int) bool) {
		for v in q.Values() {
			if !yield(v) {
				break
			}
		}
	})
}
func subject(q queue.Queue[int]) int fails queue.Closed { for v in wrap(q) { return v }; 0 }
`},
		{"pop-loop", "func subject(q queue.Queue[int]) int fails queue.Closed {\n\tfor {\n\t\tlet v = try q.Pop()\n\t\treturn v\n\t}\n}\n"},
		{"pop-capability", `type Sink {
	Put(v int) int,
}
type Keep {}
func Put(_ Keep, v int) int { v }
let Kept = context { Sink: Keep }
func take(q queue.Queue[int]) int fails queue.Closed needs Sink {
	for {
		return Sink.Put(try q.Pop())
	}
}
func subject(q queue.Queue[int]) int fails queue.Closed { with Kept { try take(q) } }
`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { deliveredNative(t, "import queue\n"+c.src+"func main() {}\n") })
	}
}

// deliveredNative runs one witness program natively against the copied
// runtime with the delivery seam.
func deliveredNative(t *testing.T, src string) {
	t.Helper()
	o := mustLower(t, "delivered", src)
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
		data := f.Data
		if f.Path == "ergo/runtime/v1/queue.go" {
			// Pop and Values each take through this line; the seam must
			// reach both, so a drift fails here.
			from := "v, ok := q.take(t.Done())"
			if n := strings.Count(string(data), from); n != 2 {
				t.Fatalf("delivery seam %q occurs %d times, want 2 (Pop, Values)", from, n)
			}
			data = []byte(strings.ReplaceAll(string(data), from, from+"\n\tif ok && DeliveredHook != nil {\n\t\tDeliveredHook(t)\n\t}"))
		}
		write(f.Path, data)
	}
	write("ergo/runtime/v1/delivered_probe.go", []byte("package v1\nimport \"ergo.local/prototype/runtime/rt\"\nvar DeliveredHook func(*rt.Task)\n"))
	write("delivered_test.go", []byte(deliveredNativeTest))
	output, err := nativeGoTest(t, dir)
	if err != nil {
		t.Fatalf("native delivered witness: %v\n%s\nErgo:\n%s\nGo:\n%s", err, output, src, stripDirectives(o.mainGo()))
	}
}

// TestNativeHarnessesHaveNoWallBound: the native harnesses wait for events
// only. A wall-clock bound in them (time.After, a deadline from time.Now)
// failed a correct run on a descheduled host
// (fu-lower-flake); a hang is the native binary's -timeout, which
// nativeGoTest derives from this binary's deadline.
func TestNativeHarnessesHaveNoWallBound(t *testing.T) {
	for name, text := range map[string]string{"blocking": blockingNativeTest, "delivered": deliveredNativeTest} {
		for _, bound := range []string{"time.After(", "time.Now()", "time.NewTimer(", "time.Since("} {
			if strings.Contains(text, bound) {
				t.Errorf("the %s native harness holds the wall-clock bound %s", name, bound)
			}
		}
	}
}

// nativeGoTest runs go test -v on the native module in dir. Its only time
// bound is this test binary's own deadline: the native binary's -timeout
// ends a minute before it, so a native hang panics there, naming the
// subtest and every goroutine's stack, and the command's context ends
// 30 s before it, after the native binary had its chance to report.
//
// A fixed wall-clock bound (40 s for compile and run, 30 s for the run)
// failed a correct program when the host descheduled the run: the gate
// runs niced under the host's load, and the go command compiles the
// copied runtime into the sweep's fresh cache (fu-lower-flake).
func nativeGoTest(t *testing.T, dir string) ([]byte, error) {
	t.Helper()
	fastTierGuard(t, "a go command")
	ctx, timeout := context.Background(), "0"
	if end, ok := t.Deadline(); ok {
		left := (time.Until(end) - time.Minute).Truncate(time.Second)
		if left < time.Second { // -timeout=0s would be no bound at all
			t.Fatalf("less than a minute of this test binary's deadline is left for a native go test")
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, end.Add(-30*time.Second))
		defer cancel()
		timeout = left.String()
	}
	cmd := exec.CommandContext(ctx, "go", "test", "-count=1", "-timeout="+timeout, "-v", ".")
	cmd.Dir = dir
	return cmd.CombinedOutput()
}

const deliveredNativeTest = `package main
import (
 "testing"
 rt "ergo.local/prototype/runtime/rt"
 ergo "ergo.local/prototype/runtime/v1"
)
func TestDelivered(t *testing.T) {
 control, waiter := rt.Root("controller"), rt.Root("waiter")
 q := ergo.New[int](1)
 if err := q.Push(control, 7); err != nil { t.Fatal(err) }
 hooked := 0
 ergo.DeliveredHook = func(task *rt.Task) { if task == waiter { hooked++; waiter.Interrupt() } }
 var value int; var err error; var panicked any
 func() { defer func() { panicked = recover() }(); value, err = subject(&Env{task: waiter}, q) }()
 ergo.DeliveredHook = nil
 if hooked != 1 { t.Fatalf("the delivery seam ran %d times on the waiter, want 1", hooked) }
 q.Close(); var rest []int
 for v := range q.Values(control) { rest = append(rest, v) }
 if panicked != nil || err != nil || value != 7 || len(rest) != 0 {
  t.Fatalf("7 delivered, then the stop requested: value %d, err %v, panic %v, queue %v; the body must receive the value", value, err, panicked, rest)
 }
 pending := func() (p any) { defer func() { p = recover() }(); waiter.Check(); return nil }()
 if pending != rt.Interrupted { t.Fatalf("the stop was lost: the next poll raised %v", pending) }
}
`

const blockingProbe = `package v1
import "ergo.local/prototype/runtime/rt"
var BlockingHook func(string, *rt.Task) func()
func BlockingEnter(name string, task *rt.Task) func() {
 if BlockingHook != nil { return BlockingHook(name, task) }
 return func() {}
}
func BlockingOwner[T any](t *Task[T]) *rt.Task {
 if t.owner == nil { return nil }
 return t.owner.s.Task()
}
func BlockingParked[T any](q *Queue[T]) int {
 q.mu.Lock(); defer q.mu.Unlock(); return q.parked
}
func BlockingEntries(s *Scope) int { return s.n }
func BlockingRuns[T any](t *Task[T]) *rt.Task { return t.h.Task() }
`

const blockingNativeTest = `package main
import (
 "runtime"
 "strings"
 "sync/atomic"
 "testing"
 "time"
 rt "ergo.local/prototype/runtime/rt"
 ergo "ergo.local/prototype/runtime/v1"
)
const primitive = PRIMITIVE
type outcome struct { value int; err error; panicValue any }
func attempt(f func() (int, error)) (o outcome) {
 defer func() { o.panicValue = recover() }()
 o.value, o.err = f(); return
}
// receive and await wait for an event with no wall-clock bound: a fixed
// bound failed correct runs on a descheduled host (fu-lower-flake). A
// primitive that never completes, drains or parks hangs here until this
// binary's -timeout, whose panic names the subtest and shows each
// goroutine's stack, this wait's line among them.
func receive[T any](t *testing.T, ch <-chan T) T {
 t.Helper(); return <-ch
}
func await(t *testing.T, test func() bool) {
 t.Helper()
 for !test() { runtime.Gosched() }
}
func handleParked(method string) bool {
 buf := make([]byte, 1<<18)
 stack := string(buf[:runtime.Stack(buf, true)])
 for _, block := range strings.Split(stack, "\n\n") {
  if (strings.Contains(block, "[select]") || strings.Contains(block, "[chan receive]")) && strings.Contains(block, "(*Handle[") && strings.Contains(block, ")."+method+"(") { return true }
 }
 return false
}
func parked(t *testing.T, q *ergo.Queue[int], clock *rt.Virtual) {
 t.Helper()
 await(t, func() bool {
  switch primitive {
  case "Sleep", "Timeline.Wait": return clock.Pending() == 1
  case "Task.Wait": return clock.Pending() == 1 && handleParked("Wait")
  case "Task.Stop": return clock.Pending() == 1 && handleParked("Stop")
  default: return ergo.BlockingParked(q) == 1
  }
 })
}
func TestBlocking(t *testing.T) {
 for _, phase := range []string{"before-entry", "while-parked", "concurrent-ready"} {
  for _, masked := range []bool{false, true} {
   owners := []bool{false}; if strings.HasPrefix(primitive,"Task.") { owners = append(owners,true) }
   for _, moved := range owners {
    label := phase+"/unmasked/direct"; if masked { label = phase+"/masked/direct" }; if moved { label += "/returned-owner" }
    t.Run(label, func(t *testing.T) { drive(t, phase, masked, moved) })
   }
  }
 }
 if primitive == "Task.Wait" {
  for _, moved := range []bool{false,true} { t.Run("ready-before-stopped-entry",func(t *testing.T) { drive(t,"ready-before-stopped-entry",false,moved) }) }
 }
}
func drive(t *testing.T, phase string, masked, moved bool) {
 control, waiter := rt.Root("controller"), rt.Root("waiter")
 q := ergo.New[int](1)
 if primitive == "Queue.Push" { if err := q.Push(control, 99); err != nil { t.Fatal(err) } }
 clock := rt.NewVirtual(time.Unix(0, 0)); rt.BlockingClock = clock
 rt.BlockingFinished = make(chan *rt.Task, 8)
 entered, release := make(chan *rt.Task, 1), make(chan struct{})
 var once atomic.Bool
 ergo.BlockingHook = func(name string, task *rt.Task) func() {
  if name == primitive && once.CompareAndSwap(false, true) { entered <- task; <-release }
  return func() {}
 }
 done, finished, childDone := make(chan outcome, 1), make(chan struct{}), make(chan struct{})
 go func() {
  defer close(finished)
  if masked { mask := waiter.Mask(); defer mask.Unmask() }
  first, destination := ergo.NewScope(waiter), ergo.NewScope(waiter)
  defer func() { if !moved || !strings.HasPrefix(primitive,"Task.") { _ = attempt(func() (int,error) { first.End(nil); return 0,nil }) }; _ = attempt(func() (int,error) { destination.End(nil); return 0,nil }) }()
  var h *ergo.Task[int]
  if strings.HasPrefix(primitive, "Task.") {
   h = ergo.Go(&first, "child", func(ct *rt.Task) (int,error) {
    defer close(childDone)
    if primitive == "Task.Stop" { mask := ct.Mask(); defer mask.Unmask() }
    if err := rt.Sleep(ct, clock, time.Hour); err != nil { panic(err) }; return 7,nil
   })
   if moved {
    h = h.Move(&destination)
    if ergo.BlockingEntries(&first) != 0 || ergo.BlockingEntries(&destination) != 1 { done <- outcome{panicValue:"Move kept the first owner"}; return }
    first.End(nil)
   }
  }
  _ = h
  out := attempt(func() (int,error) { return subject(&Env{task:waiter}, SUBJECT_ARG) })
  if h != nil && (ergo.BlockingEntries(&first) != 0 || ergo.BlockingEntries(&destination) != 0) { out.panicValue = "consumed handle retained by its owner" }
  done <- out
 }()
 if actual := receive(t, entered); actual != waiter { t.Fatal("binding received another task") }
 ready := func() {
  switch primitive {
  case "Sleep", "Timeline.Wait", "Task.Wait", "Task.Stop": clock.Advance(time.Hour)
  case "Queue.Push": if v, err := q.Pop(control); err != nil || v != 99 { panic("lost original queue value") }
  default: if err := q.Push(control, 7); err != nil { panic(err) }
  }
 }
 madeReady := false
 if phase == "ready-before-stopped-entry" {
  await(t,func() bool { return clock.Pending() == 1 }); ready(); receive(t,rt.BlockingFinished)
  madeReady = true; waiter.Interrupt()
 }
 if phase == "before-entry" { waiter.Interrupt() }
 close(release)
 switch {
 case phase == "concurrent-ready":
  parked(t, q, clock)
  start, stopped, becameReady := make(chan struct{}), make(chan struct{}), make(chan struct{})
  go func() { <-start; waiter.Interrupt(); close(stopped) }()
  go func() { <-start; ready(); close(becameReady) }()
  close(start); receive(t, stopped); receive(t, becameReady); madeReady = true
 case phase == "while-parked":
  parked(t, q, clock); waiter.Interrupt()
  if masked || primitive == "Task.Stop" { ready(); madeReady = true }
 case masked || primitive == "Task.Stop":
  parked(t, q, clock); ready(); madeReady = true
 }
 out := receive(t, done); receive(t, finished)
 if strings.HasPrefix(primitive,"Task.") { receive(t,childDone); if phase != "ready-before-stopped-entry" { receive(t,rt.BlockingFinished) } }
 interrupted := out.panicValue == rt.Interrupted
 if out.err != nil || (out.panicValue != nil && !interrupted) { t.Fatalf("exit = %+v; interruption must not be a typed failure or defect", out) }
 if masked || primitive == "Task.Stop" {
  if interrupted { t.Fatalf("masked or draining primitive was interrupted: %+v", out) }
 } else if phase != "concurrent-ready" && !interrupted { t.Fatalf("stopped primitive delivered a value: %+v", out) }
 if !interrupted { want := 7; if primitive == "Task.Stop" { want = 0 }; if out.value != want { t.Fatalf("value %d, want %d", out.value,want) } }
 if clock.Pending() != 0 { t.Fatalf("%d retained timers", clock.Pending()) }
 q.Close(); var remaining []int
 for v := range q.Values(control) { remaining = append(remaining,v) }
 count := 0; for _, v := range remaining { if v == 7 { count++ } }
 if primitive == "Queue.Push" {
  want := 1; if interrupted { want = 0 }; if count != want { t.Fatalf("stopped/accepted Push conservation: %+v, queue %v",out,remaining) }
 } else if primitive == "Queue.Pop" || primitive == "Queue.Values" {
  want := 0; if madeReady && interrupted { want = 1 }; if count != want { t.Fatalf("stopped Pop/Values took a value: %+v, queue %v",out,remaining) }
 }
}
// The returned Task must run with its own environment. The entry barrier
// is after all source-entry polls, so those polls cannot hide a wrong
// primitive task. Stop is observed before the barrier is released.
func TestReturnedTaskEnvironment(t *testing.T) {
 parent := rt.Root("parent"); destination := ergo.NewScope(parent)
 q := ergo.New[int](1)
 if primitive == "Queue.Push" { _ = q.Push(parent,99) }
 clock := rt.NewVirtual(time.Unix(0,0)); rt.BlockingClock = clock
 rt.BlockingFinished = nil
 entered, release := make(chan *rt.Task,1), make(chan struct{})
 var once atomic.Bool
 ergo.BlockingHook = func(name string, task *rt.Task) func() {
  if name == primitive && once.CompareAndSwap(false,true) { entered <- task; <-release }
  return func() {}
 }
 h := launch(&Env{task:parent}, &destination,q)
 child := receive(t,entered)
 if child != ergo.BlockingRuns(h) { t.Fatal("returned Task's primitive does not use the task the returned Task runs") }
 released := make(chan struct{})
 go func() { receive(t,child.Done()); close(release); close(released) }()
 h.Stop(); receive(t,released)
 if clock.Pending() != 0 || ergo.BlockingEntries(&destination) != 0 { t.Fatal("returned Task retained a timer or owner entry") }
 destination.End(nil)
}
`
