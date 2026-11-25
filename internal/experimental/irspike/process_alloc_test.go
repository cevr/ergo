package irspike

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// TestIRUntimedProcessAllocations checks that a nonpositive time limit
// costs no allocations beyond starting and observing the same child.
// It builds Go because heap counts include Go's escape analysis and the
// runtime's scope registrations; the checked program cannot see them.
func TestIRUntimedProcessAllocations(t *testing.T) {
	_, tc := needsBuild(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "process.ergo")
	if err := os.WriteFile(src, []byte(untimedProcess), 0o644); err != nil {
		t.Fatal(err)
	}
	u, err := Load(src)
	if err != nil {
		t.Fatal(err)
	}
	mod, bin := filepath.Join(dir, "ir"), filepath.Join(dir, "alloc")
	rep, err := Emit(u, IR, mod)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(rep.IR, "process__Run") {
		t.Fatalf("process.Run has no IR body: %v", rep.Fallback)
	}
	if err := os.WriteFile(filepath.Join(mod, "count.go"), []byte(untimedProcessCounter), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Build(tc, IR, mod, bin, BuildOptions{}); err != nil {
		t.Fatal(err)
	}
	got := run(t, bin, "GOGC=off", "GOMAXPROCS=1")
	var zero, negative, child float64
	if _, err := fmt.Sscan(got.stdout, &zero, &negative, &child); got.exit != 0 || err != nil {
		t.Fatalf("counting process allocations: %+v (%v)", got, err)
	}
	t.Logf("allocations per run: zero %.3f, negative %.3f, start and observe child %.3f", zero, negative, child)
	if zero > child || negative > child {
		t.Fatalf("a run without a deadline allocates more than starting and observing its child")
	}
}

const untimedProcess = `import process
import task

func measured(d duration, f own func() int) int needs Scheduler {
	process.Run(process.Limits{..process.Unlimited, Time: d}, f) catch {
	case process.OverTime, process.OverSteps, process.Crashed:
		-1
	}
}

func observed(f own func() int) int {
	let child = task.Go() { f() }
	match task.ExitOf(child) {
	case task.Done[int] as d:
		d.Value
	case _:
		-1
	}
}

func main() needs Ambient {
	with context { Scheduler: NewLiveScheduler() } { let _ = measured(0ns, func() int { 3 }) }
	let _ = observed(func() int { 3 })
}
`

const untimedProcessCounter = `package main

import (
	"fmt"
	"testing"
	"time"

	"ergo.local/prototype/runtime/rt"
	"ergo.local/prototype/runtime/v1"
)

type unusedScheduler struct{}

func (unusedScheduler) Sleep(*rt.Task, time.Duration) { panic("unexpected sleep") }
func (unusedScheduler) Alarms(*rt.Task) *v1.AlarmClock { panic("unexpected clock access") }

func init() {
	_, err := rt.Run(rt.Root("untimed process allocs"), func(task *rt.Task) (struct{}, error) {
		env := &Env{task: task, Scheduler: unusedScheduler{}}
		f := func(*Env) (int, error) { return 3, nil }
		count := func(run func() int) float64 {
			return testing.AllocsPerRun(1000, func() {
				if run() != 3 { panic("wrong child value") }
			})
		}
		fmt.Println(count(func() int { return measured(env, 0*time.Nanosecond, f) }),
			count(func() int { return measured(env, -time.Nanosecond, f) }),
			count(func() int { return observed(env, f) }))
		return struct{}{}, nil
	})
	if err != nil { panic(err) }
}
`
