package lower

import (
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/std"
	v1 "ergo.local/prototype/runtime/v1"
)

// Stage 4 (S4c item 6): timeout.After lowers to its runtime/v1 body,
// v1.After, which takes the caller's Scheduler as a hidden argument
// (Q-S4-7; docs/spec/stdlib.md, Hidden arguments), so the deadline is the
// program's time capability and not a host timer.

// TestTimeoutAfterLowers: a body that ends in time gives its value, and a
// body that sleeps past its deadline fails with timeout.Expired, which a
// catch arm of that type handles (runtime.md, Timeouts 4). It is the F
// timeout leg of the acceptance, with the LiveScheduler.
func TestTimeoutAfterLowers(t *testing.T) {
	needsBuild(t)
	wantRun(t, `import timeout

func quick() int { 42 }

func slow() int needs Scheduler {
	Scheduler.Sleep(2s)
	7
}

func run() needs Console | Scheduler {
	let a = timeout.After(5s) { quick() } catch {
	case timeout.Expired:
		-1
	}
	let b = timeout.After(10ms) { slow() } catch {
	case timeout.Expired:
		-2
	}
	Console.Println("\(a) \(b)")
}

func main() needs Console | Ambient {
	with context { Scheduler: NewLiveScheduler() } {
		run()
	}
}
`, "42 -2\n")
}

// timeoutProgram has an hour's deadline whose body sleeps an hour on a
// time.Manual that no one advances, which its own with binds, so only the
// deadline's Scheduler, the one the caller of run binds, decides the race.
const timeoutProgram = `import time
import timeout

func slow() int needs Scheduler {
	Scheduler.Sleep(1h)
	7
}

func quick() int { 42 }

func late() int needs Scheduler {
	timeout.After(1h) {
		with context { Scheduler: time.NewManual(0) } {
			slow()
		}
	} catch {
	case timeout.Expired:
		-1
	}
}

func early() int needs Scheduler {
	timeout.After(1h) { quick() } catch {
	case timeout.Expired:
		-1
	}
}
`

// TestTimeoutDeadlineIsTheProgramScheduler is the Q-S4-7 witness. A Go
// driver in the emitted module binds its own Scheduler, as a test
// Scheduler provider is bound, and calls the lowered functions:
//   - a Scheduler whose Sleep ends at once makes the hour's deadline pass
//     at once, so late() is -1 without an hour of wall time, and the
//     Scheduler was asked for exactly the hour the source names;
//   - a Scheduler whose Sleep waits until its task is stopped never makes
//     the deadline pass, so early() is 42, and After stops that sleep
//     before it returns: no deadline child outlives the call.
//
// A host timer in After would leave late() waiting an hour (the test's
// watchdog fails it), and a deadline that ignores the Scheduler would never
// ask it for the hour.
// Builds Go because only executing the deadline with distinct Scheduler
// providers checks which clock wins and that its child is drained.
func TestTimeoutDeadlineIsTheProgramScheduler(t *testing.T) {
	needsBuild(t)
	prog := lowerSource("deadline", timeoutProgram, false)
	if len(prog.diags) != 0 || prog.err != nil || prog.panicked != nil || len(prog.refused) != 0 {
		t.Fatalf("lowering: diags %v, err %v, panic %v, refused %v", prog.diags, prog.err, prog.panicked, prog.refused)
	}
	driver := `package main

import (
	"sync"
	"testing"
	"time"

	rt "ergo.local/prototype/runtime/rt"
	ergo "ergo.local/prototype/runtime/v1"
)

// instant ends every Sleep at once and records what it was asked.
type instant struct {
	mu    sync.Mutex
	asked []time.Duration
}

func (s *instant) Sleep(_ *rt.Task, d time.Duration) {
	s.mu.Lock()
	s.asked = append(s.asked, d)
	s.mu.Unlock()
}

// never waits until its task is stopped, and counts its open sleeps.
type never struct {
	mu   sync.Mutex
	open int
	all  int
}

func (s *never) Sleep(t *rt.Task, d time.Duration) {
	s.mu.Lock()
	s.open++
	s.all++
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.open--
		s.mu.Unlock()
	}()
	<-t.Done()
	panic(rt.Interrupted)
}

// within runs f and waits for its end, an event: an After whose deadline
// is not the program's Scheduler sleeps an hour, and go test's -timeout
// panic names TestDeadline (fu-time-limits: no fixed bound).
func within(t *testing.T, f func()) {
	t.Helper()
	f()
}

var _ ergo.Sleeper = Scheduler(nil)

func TestDeadline(t *testing.T) {
	s := &instant{}
	within(t, func() {
		_, err := rt.Run(rt.Root("late"), func(task *rt.Task) (struct{}, error) {
			if v := late(&Env{task: task, Scheduler: s}); v != -1 {
				t.Errorf("late() = %d, want -1", v)
			}
			return struct{}{}, nil
		})
		if err != nil {
			t.Error(err)
		}
	})
	if len(s.asked) != 1 || s.asked[0] != time.Hour {
		t.Errorf("the Scheduler was asked %v, want [1h]", s.asked)
	}
	n := &never{}
	within(t, func() {
		_, err := rt.Run(rt.Root("early"), func(task *rt.Task) (struct{}, error) {
			if v := early(&Env{task: task, Scheduler: n}); v != 42 {
				t.Errorf("early() = %d, want 42", v)
			}
			return struct{}{}, nil
		})
		if err != nil {
			t.Error(err)
		}
	})
	if n.all != 1 || n.open != 0 {
		t.Errorf("deadline sleeps: %d started, %d open after After returned; want 1, 0", n.all, n.open)
	}
}
`
	goTestEmitted(t, prog, driver)
}

// TestStdExternsHaveBodies is the guard of the internal error in useFunc:
// every extern of every std package is an intrinsic of the lowering or has
// a runtime/v1 body that v1.BodyOf reads, or binds a Go builtin, so no program reaches a std
// function the lowering cannot call. A body that takes the caller's
// Scheduler needs a Scheduler in the Ergo signature (hiddenArgs).
func TestStdExternsHaveBodies(t *testing.T) {
	paths := std.Packages()
	if len(paths) == 0 {
		t.Fatal("no std packages")
	}
	seen := 0
	for _, path := range paths {
		pkg, err := check.Std.Import(path)
		if err != nil {
			t.Fatalf("check.Std.Import(%q): %v", path, err)
		}
		for _, f := range pkg.Funcs {
			if f.Decl == nil || f.Decl.Extern == nil {
				continue
			}
			seen++
			if check.GoBuiltin(f) != "" {
				continue // written at its call, Go's builtin (builtinCall)
			}
			target, name := f.Decl.Extern.Split()
			if target != runtimeImport {
				t.Errorf("%s.%s binds %s.%s, not a runtime/v1 body", path, f.Name, target, name)
				continue
			}
			body, err := v1.BodyOf(name)
			if err != nil {
				t.Errorf("%s.%s: %v", path, f.Name, err)
				continue
			}
			if hasHidden(body.Hidden, v1.CallerScheduler) {
				found := false
				for _, e := range rowElems(f.Sig.Uses) {
					found = found || isPreludeType(e, "Scheduler")
				}
				if !found {
					t.Errorf("%s.%s takes the caller's Scheduler, and its signature needs no Scheduler", path, f.Name)
				}
			}
		}
	}
	if seen == 0 {
		t.Fatal("no std extern seen")
	}
}
