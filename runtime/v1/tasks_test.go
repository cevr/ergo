package v1

import (
	"context"
	"fmt"
	"math/rand/v2"
	"runtime"
	"slices"
	"sync"
	"testing"
	"time"

	"ergo.local/prototype/internal/lawledger"
	"ergo.local/prototype/runtime/rt"
)

// elemFailed is the typed failure of a generated element of
// TestTransformModel.
type elemFailed struct{ i int }

func (e elemFailed) Error() string { return fmt.Sprintf("element %d failed", e.i) }

// transformCase is a generated tasks.Transform run: the limit, and per
// element its sleep in seconds and whether it fails. The sleeps are
// distinct powers of two, so the end of every call, a sum of the sleeps
// of one chain of calls on one slot, is a distinct instant: the order in
// which calls end is the model's, with no tie for the scheduler to break.
type transformCase struct {
	limit int
	sleep []int64
	fails []bool
}

// transformOutcome is what a run shows: the results, or the index of the
// failure it ended with (-1 for none); the elements whose call started;
// the most calls running at once; and the clock when Transform returned.
type transformOutcome struct {
	out     []int
	failed  int
	started []bool
	most    int
	at      int64
}

// TestTransformModel is the model check of tasks.Transform (the red
// acceptance check of batch-12 C2). 300 generated cases of 0 to 8
// elements and limits of 1 to 5 run on a VirtualClock (C1) against a
// discrete-event model: up to limit calls start, the call that ends first
// is seen first, a success starts the next element, and the first failure
// seen ends the map at once. The run must give the model's results in the
// order of xs, or its failure; start exactly the model's elements (none
// after the failure); reach the model's count of calls at once, never more
// than limit; return at the model's instant (the calls still running are
// stopped, not waited out); and leave no call running. A failing seed
// prints the case.
func TestTransformModel(t *testing.T) {
	before := runtime.NumGoroutine()
	for seed := uint64(0); seed < 300; seed++ {
		c := transformDraw(seed)
		want := transformModel(c)
		got := transformRun(c)
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("seed %d: case %+v\nrun   %+v\nmodel %+v", seed, c, got, want)
		}
	}
	waitFor(t, "goroutines to end", func() bool { return runtime.NumGoroutine() <= before })
}

func transformDraw(seed uint64) transformCase {
	r := rand.New(rand.NewPCG(seed, 21))
	n := r.IntN(9)
	c := transformCase{limit: 1 + r.IntN(5), sleep: make([]int64, n), fails: make([]bool, n)}
	for i, p := range r.Perm(n) {
		c.sleep[i] = 1 << p
		c.fails[i] = r.IntN(5) == 0
	}
	return c
}

// transformModel runs c as a discrete-event model.
func transformModel(c transformCase) transformOutcome {
	n := len(c.sleep)
	o := transformOutcome{out: make([]int, n), failed: -1, started: make([]bool, n)}
	end := map[int]int64{} // the running calls and when each ends
	var now int64
	next := 0
	for {
		for len(end) < c.limit && next < n {
			end[next] = now + c.sleep[next]
			o.started[next] = true
			next++
		}
		o.most = max(o.most, len(end))
		if len(end) == 0 {
			break
		}
		first := -1
		for i, e := range end {
			if first < 0 || e < end[first] {
				first = i
			}
		}
		now = end[first]
		delete(end, first)
		if c.fails[first] {
			o.failed = first
			o.out = nil
			break
		}
		o.out[first] = first * 10
	}
	o.at = now
	return o
}

// transformRun runs c through TasksTransform on a root that made a
// VirtualClock's timeline.
func transformRun(c transformCase) transformOutcome {
	n := len(c.sleep)
	root := rt.Root("model")
	line := NewVirtualTimeline(root, 0, 0)
	o := transformOutcome{failed: -1, started: make([]bool, n)}
	var mu sync.Mutex
	running := 0
	xs := make([]int, n)
	for i := range xs {
		xs[i] = i
	}
	out, err := TasksTransform(Site("model"), root, xs, c.limit, func(ct *rt.Task, i int) (int, error) {
		mu.Lock()
		o.started[i] = true
		running++
		o.most = max(o.most, running)
		mu.Unlock()
		defer func() {
			mu.Lock()
			running--
			mu.Unlock()
		}()
		line.Wait(ct, time.Duration(c.sleep[i])*time.Second)
		if c.fails[i] {
			return 0, elemFailed{i}
		}
		return i * 10, nil
	})
	o.out = out
	if f, ok := err.(elemFailed); ok {
		o.failed = f.i
	} else if err != nil {
		panic(err)
	}
	o.at = line.Read().Mono / int64(time.Second)
	mu.Lock()
	defer mu.Unlock()
	if running != 0 {
		o.most = -running // a call still running after Transform returned
	}
	return o
}

// TestTasksLaws runs the laws of std tasks against TasksTransform
// (docs/spec/stdlib.md, Std laws), 200 draws each.
func TestTasksLaws(t *testing.T) {
	draw := func(r *rand.Rand) ([]int, int) {
		xs := make([]int, r.IntN(12))
		for i := range xs {
			xs[i] = r.IntN(1000) - 500
		}
		k := r.Int()
		return xs, 1 + (k%4+4)%4
	}
	lawledger.Run(t, map[string]lawledger.Law{
		"tasks TransformEqualsLoop": {Statement: `TransformEqualsLoop(xs []int, k int) { let got = Transform(xs, limitOf(k), func(x int) int { x*3 - 1 }) let mut ok = len(got) == len(xs) for i in 0..len(xs) { ok = ok && got[i] == xs[i]*3-1 } ok }`, Check: func(t *testing.T) {
			r := rand.New(rand.NewPCG(1, 22))
			for range 200 {
				xs, limit := draw(r)
				got, err := TasksTransform(Site("law"), rt.Root("law"), xs, limit, func(_ *rt.Task, x int) (int, error) { return x*3 - 1, nil })
				if err != nil || len(got) != len(xs) {
					t.Fatalf("Transform(%v, %d) = %v, %v", xs, limit, got, err)
				}
				for i, x := range xs {
					if got[i] != x*3-1 {
						t.Fatalf("Transform(%v, %d) = %v", xs, limit, got)
					}
				}
			}
		}},
		"tasks TransformKeepsOrder": {Statement: `TransformKeepsOrder(xs []int, k int) { let c = testing.NewVirtualClock(0) with context { Scheduler: c, time.Clock: c } { let n = len(xs) let mut idx = []int{} for i in 0..n { idx = idx.Append(i) } let got = Transform(idx, limitOf(k), func(i int) int needs Scheduler { Scheduler.Sleep(duration(int64(n - i))) xs[i] }) let mut ok = len(got) == n for i in 0..n { ok = ok && got[i] == xs[i] } ok } }`, Check: func(t *testing.T) {
			r := rand.New(rand.NewPCG(1, 23))
			for range 200 {
				xs, limit := draw(r)
				root := rt.Root("law")
				line := NewVirtualTimeline(root, 0, 0)
				idx := make([]int, len(xs))
				for i := range idx {
					idx[i] = i
				}
				got, err := TasksTransform(Site("law"), root, idx, limit, func(ct *rt.Task, i int) (int, error) {
					line.Wait(ct, time.Duration(len(xs)-i))
					return xs[i], nil
				})
				if err != nil || !slices.Equal(got, xs) {
					t.Fatalf("Transform with reversed ends over %v, limit %d = %v, %v", xs, limit, got, err)
				}
			}
		}},
		"tasks LimitHolds": {Statement: `LimitHolds(xs []int, k int) { let limit = limitOf(k) let c = testing.NewVirtualClock(0) let running = sync.NewRef[int](0) let most = sync.NewRef[int](0) with context { Scheduler: c, time.Clock: c } { let got = Transform(xs, limit, func(x int) int needs Scheduler { let now = sync.Modify(running, func(v int) (int, int) { (v + 1, v + 1) }) most.Update(func(m int) int { if now > m { return now } m }) Scheduler.Sleep(1s) running.Update(func(v int) int { v - 1 }) x }) let want = if len(xs) < limit { len(xs) } else { limit } len(got) == len(xs) && most.Get() == want && running.Get() == 0 } }`, Check: func(t *testing.T) {
			r := rand.New(rand.NewPCG(1, 24))
			for range 200 {
				xs, limit := draw(r)
				root := rt.Root("law")
				line := NewVirtualTimeline(root, 0, 0)
				var mu sync.Mutex
				running, most := 0, 0
				got, err := TasksTransform(Site("law"), root, xs, limit, func(ct *rt.Task, x int) (int, error) {
					mu.Lock()
					running++
					most = max(most, running)
					mu.Unlock()
					line.Wait(ct, time.Second)
					mu.Lock()
					running--
					mu.Unlock()
					return x, nil
				})
				if err != nil || len(got) != len(xs) || most != min(len(xs), limit) || running != 0 {
					t.Fatalf("Transform of %d with limit %d: %d at most at once, %d left, err %v", len(xs), limit, most, running, err)
				}
			}
		}},
	})
}

// TestTransformCallerStopped: the childWait laws of tasks.Transform
// (lower runtime199 childWaitLaws), limit 2. The first call stops the
// caller and returns.
//
// An interruptible caller starts no call after the stop, stops and drains
// the running ones, and raises the interruption: the other calls wait
// until they are stopped. The caller may see the first call's end before
// the stop (a select of two ready channels picks either), so the case runs
// 20 times to reach both orders.
//
// A masked caller runs Transform to its end, the stop pending, so every
// call returns and every result is there. The second call waits until the
// third has started, so the caller waits for an end with none queued after
// the stop has come: a masked wait must not end on it.
func TestTransformCallerStopped(t *testing.T) {
	for run := range 21 {
		masked := run == 20
		root := rt.Root("main")
		var mu sync.Mutex
		started, running := 0, 0
		third := make(chan struct{})
		var out []int
		var outErr error
		var err error
		within(t, fmt.Sprintf("Transform under a stopped caller (masked %v)", masked), func() {
			err = runOn(root, func(root *rt.Task) error {
				if masked {
					m := root.Mask()
					defer m.Unmask()
				}
				out, outErr = TasksTransform(Site("test"), root, []int{0, 1, 2, 3, 4}, 2, func(ct *rt.Task, i int) (int, error) {
					mu.Lock()
					started++
					running++
					mu.Unlock()
					defer func() {
						mu.Lock()
						running--
						mu.Unlock()
					}()
					switch {
					case i == 0:
						root.Interrupt()
					case !masked:
						sleepUntilStopped(ct)
					case i == 1:
						<-third
					case i == 2:
						close(third)
					}
					return i * 10, nil
				})
				return nil
			})
		})
		switch {
		case running != 0:
			t.Errorf("masked %v: %d calls still running after Transform", masked, running)
		case !masked && (err != rt.Interrupted || started != 2):
			t.Errorf("unmasked: exit %v after %d calls started; want the interruption after 2", err, started)
		case masked && (outErr != nil || fmt.Sprint(out) != "[0 10 20 30 40]"):
			t.Errorf("masked: Transform = %v, %v; want every result", out, outErr)
		}
	}
}

// BenchmarkTasksTransform runs TasksTransform over 64 ints with limit 8
// and a trivial f; BenchmarkTasksTransformHandGo is its control, the
// errgroup shape written by hand.
func BenchmarkTasksTransform(b *testing.B) {
	xs := make([]int, 64)
	root := rt.Root("bench")
	b.ReportAllocs()
	for b.Loop() {
		if _, err := TasksTransform(Site("bench"), root, xs, 8, func(_ *rt.Task, x int) (int, error) { return x + 1, nil }); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkTasksTransformHandGo: a semaphore channel of 8, a WaitGroup, a
// context.WithCancel that the first error cancels, results by index (the
// shape of errgroup.Group with SetLimit).
func BenchmarkTasksTransformHandGo(b *testing.B) {
	xs := make([]int, 64)
	b.ReportAllocs()
	for b.Loop() {
		ctx, cancel := context.WithCancel(context.Background())
		out := make([]int, len(xs))
		sem := make(chan struct{}, 8)
		var wg sync.WaitGroup
		var once sync.Once
		var first error
		for i, x := range xs {
			sem <- struct{}{}
			if ctx.Err() != nil {
				<-sem
				break
			}
			wg.Add(1)
			go func() {
				defer func() { <-sem; wg.Done() }()
				v, err := x+1, error(nil)
				if err != nil {
					once.Do(func() { first = err; cancel() })
					return
				}
				out[i] = v
			}()
		}
		wg.Wait()
		cancel()
		if first != nil {
			b.Fatal(first)
		}
	}
}

// TestTransformLimitDefect: a limit below 1 is a defect of the caller, not
// a failure, and starts no call.
func TestTransformLimitDefect(t *testing.T) {
	for _, limit := range []int{0, -3} {
		calls := 0
		p := func() (p any) {
			defer func() { p = recover() }()
			_, _ = TasksTransform(Site("defect"), rt.Root("defect"), []int{1}, limit, func(*rt.Task, int) (int, error) {
				calls++
				return 0, nil
			})
			return nil
		}()
		if calls != 0 || fmt.Sprint(p) != fmt.Sprintf("tasks.Transform: limit %d is less than 1", limit) {
			t.Fatalf("limit %d: recovered %v after %d calls", limit, p, calls)
		}
	}
}
