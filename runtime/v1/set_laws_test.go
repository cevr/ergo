package v1

import (
	"math/rand/v2"
	"sync/atomic"
	"testing"
	"time"

	"ergo.local/prototype/internal/lawledger"
	"ergo.local/prototype/runtime/rt"
)

// lawFailed is the failure of a member in the Set laws.
type lawFailed struct{}

func (lawFailed) Error() string { return "lawFailed" }

// setLawsLimit is the law's limitOf: 1 to 4, drawn from n.
func setLawsLimit(n int) int { return 1 + (n%4+4)%4 }

// inSetFrame runs body in a frame of root that owns a Set of the policy
// kind and limit, and returns the frame's exit.
func inSetFrame(root *rt.Task, kind, limit int, body func(set *Set)) error {
	return setCatch(root, func() {
		fs := NewScope(root)
		defer fs.End(nil)
		body(ownedSet(&fs, kind, limit))
	})
}

// setLaws are the Go restatements of the Set laws of std tasks, which
// TestTasksLaws runs, 200 draws each.
func setLaws() map[string]lawledger.Law {
	return map[string]lawledger.Law{
		"tasks NoMemberOutlivesItsFrame": {Statement: `NoMemberOutlivesItsFrame(n int) { let k = limitOf(n) let c = testing.NewVirtualClock(0) let started = sync.NewRef[int](0) let running = sync.NewRef[int](0) with context { Scheduler: c, time.Clock: c } { let start = time.Clock.Now() sleepers(k, started, running) started.Get() == k && running.Get() == 0 && time.Clock.Now().Sub(start) == 1s } }`, Check: func(t *testing.T) {
			r := rand.New(rand.NewPCG(1, 41))
			for range 200 {
				k := setLawsLimit(r.Int())
				root := rt.Root("law")
				line := NewVirtualTimeline(root, 0, 0)
				var started, running atomic.Int64
				err := inSetFrame(root, setFailFast, 0, func(set *Set) {
					for range k {
						_ = TasksGo(Site("law"), root, set, func(ct *rt.Task) error {
							started.Add(1)
							running.Add(1)
							defer running.Add(-1)
							line.Wait(ct, time.Hour)
							return nil
						})
					}
					line.Wait(root, time.Second)
				})
				if err != nil || started.Load() != int64(k) || running.Load() != 0 || line.Read().Mono != int64(time.Second) {
					t.Fatalf("%d sleepers: started %d, running %d at %d, %v", k, started.Load(), running.Load(), line.Read().Mono, err)
				}
			}
		}},
		"tasks FinishedAreDropped": {Statement: `FinishedAreDropped(n int) { let k = limitOf(n) let c = testing.NewVirtualClock(0) with context { Scheduler: c, time.Clock: c } { let set = NewSet(FailFast) for i in 0..k { Go(set, func() needs Scheduler { Scheduler.Sleep(duration(int64(i + 1))) }) catch { case Closed: } } let before = set.Len() Scheduler.Sleep(duration(int64(k + 1))) before == k && set.Len() == 0 } }`, Check: func(t *testing.T) {
			r := rand.New(rand.NewPCG(1, 42))
			for range 200 {
				k := setLawsLimit(r.Int())
				root := rt.Root("law")
				line := NewVirtualTimeline(root, 0, 0)
				var before, after int
				err := inSetFrame(root, setFailFast, 0, func(set *Set) {
					for i := range k {
						_ = TasksGo(Site("law"), root, set, func(ct *rt.Task) error {
							line.Wait(ct, time.Duration(i+1))
							return nil
						})
					}
					before = set.Len()
					line.Wait(root, time.Duration(k+1))
					after = set.Len()
				})
				if err != nil || before != k || after != 0 {
					t.Fatalf("%d members: Len %d, then %d, %v", k, before, after, err)
				}
			}
		}},
		"tasks FailFastStopsTheRest": {Statement: `FailFastStopsTheRest(n int) { let k = limitOf(n) let c = testing.NewVirtualClock(0) with context { Scheduler: c, time.Clock: c } { let set = NewSet[setFailed](FailFast) let start = time.Clock.Now() for _ in 0..k { Go(set, func() needs Scheduler { Scheduler.Sleep(1h) }) catch { case Closed: } } Go(set, func() fails setFailed needs Scheduler { Scheduler.Sleep(1s) return setFailed }) catch { case Closed: } set.Wait() catch { case setFailed: return time.Clock.Now().Sub(start) == 1s && set.Len() == 0 && closedNow(set) } false } }`, Check: func(t *testing.T) {
			r := rand.New(rand.NewPCG(1, 43))
			for range 200 {
				k := setLawsLimit(r.Int())
				root := rt.Root("law")
				line := NewVirtualTimeline(root, 0, 0)
				var waited, closed error
				var left int
				err := inSetFrame(root, setFailFast, 0, func(set *Set) {
					for range k {
						_ = TasksGo(Site("law"), root, set, func(ct *rt.Task) error {
							line.Wait(ct, time.Hour)
							return nil
						})
					}
					_ = TasksGo(Site("law"), root, set, func(ct *rt.Task) error {
						line.Wait(ct, time.Second)
						return lawFailed{}
					})
					waited = set.Wait(root)
					left = set.Len()
					closed = TasksGo(Site("law"), root, set, func(*rt.Task) error { return nil })
				})
				if err != nil || waited != (lawFailed{}) || line.Read().Mono != int64(time.Second) || left != 0 || closed != (TasksClosed{}) {
					t.Fatalf("%d sleepers: Wait %v at %d, Len %d, Go %v, %v", k, waited, line.Read().Mono, left, closed, err)
				}
			}
		}},
		"tasks RestartRunsAgain": {Statement: `RestartRunsAgain(n int) { let limit = limitOf(n) - 1 let runs = sync.NewRef[int](0) let set = NewSet(Restart{Limit: limit}) Go(set, func() defects { let r = sync.Modify(runs, func(v int) (int, int) { (v + 1, v + 1) }) if r <= limit { die boundedRestartFault{Run: r} } }) catch { case Closed: } set.Wait() let exhausted = task.Go() { let s = NewSet(Restart{Limit: limit}) let attempts = sync.NewRef[int](0) Go(s, func() defects { let r = sync.Modify(attempts, func(v int) (int, int) { (v + 1, v + 1) }) die boundedRestartFault{Run: r} }) catch { case Closed: } s.Wait() true } let kept = match task.ExitOf(exhausted) { case task.Defected as d: task.ReasonAs[boundedRestartFault](d) == Some[boundedRestartFault]{Value: boundedRestartFault{Run: limit + 1}} case _: false } runs.Get() == limit+1 && kept }`, Check: func(t *testing.T) {
			type boundedRestartFault struct{ Run int }
			r := rand.New(rand.NewPCG(1, 44))
			for range 200 {
				limit := setLawsLimit(r.Int()) - 1
				root := rt.Root("law")
				var runs atomic.Int64
				err := inSetFrame(root, setRestart, limit, func(set *Set) {
					_ = TasksGo(Site("law"), root, set, func(ct *rt.Task) error {
						n := int(runs.Add(1))
						if n <= limit {
							panic(DieAt(boundedRestartFault{Run: n}, "tasks.ergo:636:4"))
						}
						return nil
					})
					if err := set.Wait(root); err != nil {
						t.Fatal(err)
					}
				})
				if err != nil || runs.Load() != int64(limit+1) {
					t.Fatalf("limit %d: %d runs, %v", limit, runs.Load(), err)
				}
				var attempts atomic.Int64
				fs := NewScope(root)
				exhausted := Go[bool](&fs, Site("law"), func(ct *rt.Task) (bool, error) {
					s := NewScope(ct)
					defer s.End(nil)
					set := ownedSet(&s, setRestart, limit)
					_ = TasksGo(Site("law"), ct, set, func(gt *rt.Task) error {
						n := int(attempts.Add(1))
						panic(DieAt(boundedRestartFault{Run: n}, "tasks.ergo:636:4"))
					})
					_ = set.Wait(ct)
					return true, nil
				})
				kind, exitErr := TaskExit(root, exhausted)
				fs.End(nil)
				if exitErr != nil || kind != taskDefected {
					t.Fatalf("limit %d: exhausted exit (%d, %v), want taskDefected", limit, kind, exitErr)
				}
				if !DefectHas[boundedRestartFault](exhausted.defect) {
					t.Fatalf("limit %d: exhausted defect missing boundedRestartFault (text=%q)", limit, exhausted.defect.Text())
				}
				if got := DefectAs[boundedRestartFault](exhausted.defect); got.Run != limit+1 {
					t.Fatalf("limit %d: exhausted reason %+v, want Run=%d", limit, got, limit+1)
				}
			}
		}},
	}
}
