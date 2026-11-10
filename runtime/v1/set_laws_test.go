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
		body(TasksNewSet(&fs, kind, limit))
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
		"tasks RestartRunsAgain": {Statement: `RestartRunsAgain(n int) { let limit = limitOf(n) - 1 let runs = sync.NewRef[int](0) let set = NewSet(Restart{Limit: limit}) Go(set, func() { let r = sync.Modify(runs, func(v int) (int, int) { (v + 1, v + 1) }) if r <= limit { Unreachable("a defect the Set restarts") } }) catch { case Closed: } set.Wait() runs.Get() == limit + 1 }`, Check: func(t *testing.T) {
			r := rand.New(rand.NewPCG(1, 44))
			for range 200 {
				limit := setLawsLimit(r.Int()) - 1
				root := rt.Root("law")
				var runs atomic.Int64
				err := inSetFrame(root, setRestart, limit, func(set *Set) {
					_ = TasksGo(Site("law"), root, set, func(ct *rt.Task) error {
						if runs.Add(1) <= int64(limit) {
							panic("a defect the Set restarts")
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
			}
		}},
		"tasks Set.StopEndsEveryMember": {Statement: `StopEndsEveryMember() { Stop() Stop() Len() == 0 }`, Check: func(t *testing.T) {
			r := rand.New(rand.NewPCG(1, 45))
			for range 200 {
				k := r.IntN(5)
				root := rt.Root("law")
				line := NewVirtualTimeline(root, 0, 0)
				var left int
				err := inSetFrame(root, r.IntN(3), r.IntN(3), func(set *Set) {
					for range k {
						_ = TasksGo(Site("law"), root, set, func(ct *rt.Task) error {
							line.Wait(ct, time.Hour)
							return nil
						})
					}
					set.Stop(root)
					set.Stop(root)
					left = set.Len()
				})
				if err != nil || left != 0 {
					t.Fatalf("%d members: Len %d after Stop, %v", k, left, err)
				}
			}
		}},
	}
}
