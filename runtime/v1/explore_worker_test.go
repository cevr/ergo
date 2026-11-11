//go:build ergoexplore

package v1

import (
	"io"
	"runtime"
	"testing"

	"ergo.local/prototype/runtime/explore/sched"
	"ergo.local/prototype/runtime/rt"
)

// pipeWorker is a worker in process: workerServe on pipes. Close ends it
// and waits until its loop has returned.
type pipeWorker struct {
	r    *io.PipeReader
	w    *io.PipeWriter
	done chan struct{}
}

func (p *pipeWorker) Read(b []byte) (int, error)  { return p.r.Read(b) }
func (p *pipeWorker) Write(b []byte) (int, error) { return p.w.Write(b) }
func (p *pipeWorker) Close() error {
	p.w.Close()
	p.r.Close()
	<-p.done
	return nil
}

func startPipeWorker(body func(*rt.Task) error) (sched.Conn, error) {
	toR, toW := io.Pipe()
	fromR, fromW := io.Pipe()
	p := &pipeWorker{r: fromR, w: toW, done: make(chan struct{})}
	go func() {
		defer close(p.done)
		defer fromW.Close()
		_ = workerServe(toR, fromW, body)
	}()
	return p, nil
}

// TestWorkerRetainsNoTask is the worker's guard (the X6 executor
// decision): a run on the real scheduler that ends with every task ended
// leaves no goroutine behind, so a worker serves the next run with
// nothing retained; a deadlocked run is abandoned, so its worker ends and
// the pool starts a new one. It runs with the ergoexplore tag (the gate's
// explored worker step).
func TestWorkerRetainsNoTask(t *testing.T) {
	q := func() *Queue[int] { return New[int](1) }
	clean := func(t *rt.Task) error {
		queue := q()
		s := rt.NewScope(t)
		var err error
		defer s.End(&err)
		a := rt.Go(&s, "a", func(t *rt.Task) (struct{}, error) { return struct{}{}, queue.Push(t, 1) })
		b := rt.Go(&s, "b", func(t *rt.Task) (struct{}, error) { return struct{}{}, queue.Push(t, 2) })
		for range 2 {
			if _, err = queue.Pop(t); err != nil {
				return err
			}
		}
		if _, err = a.Wait(); err != nil {
			return err
		}
		_, err = b.Wait()
		return err
	}
	before := runtime.NumGoroutine()
	pool := &sched.Pool{Start: func() (sched.Conn, error) { return startPipeWorker(clean) }}
	rep := sched.DFS(pool, sched.Options{Preempt: -1, Schedules: 40, All: true, Visit: func(r sched.Run) {
		if r.Outcome.Abandoned || r.Outcome.Failure != "" {
			t.Fatalf("a clean run was abandoned or failed: %+v", r.Outcome)
		}
		// The worker's loop is the one goroutine more.
		settleGoroutines(t, before+1)
	}})
	if rep.Failure != nil || rep.Schedules < 2 {
		t.Fatalf("the search ran %d schedules, failure %+v", rep.Schedules, rep.Failure)
	}
	if pool.Started != 1 || pool.Abandoned != 0 {
		t.Fatalf("clean runs started %d workers and abandoned %d runs, want 1 and 0", pool.Started, pool.Abandoned)
	}
	pool.Close()

	stuck := func(t *rt.Task) error {
		_, err := q().Pop(t)
		return err
	}
	pool = &sched.Pool{Start: func() (sched.Conn, error) { return startPipeWorker(stuck) }}
	for range 2 {
		r := sched.Once(pool, sched.Cooperative{})
		if !r.Outcome.Abandoned || r.Outcome.Deadlock == nil {
			t.Fatalf("a deadlocked run was not abandoned: %+v", r.Outcome)
		}
	}
	if pool.Started != 2 || pool.Abandoned != 2 {
		t.Fatalf("two deadlocked runs started %d workers and abandoned %d runs, want 2 and 2", pool.Started, pool.Abandoned)
	}
}

// settleGoroutines yields until at most want goroutines are live: the goroutines of
// a run's ended tasks return just after the run does. It counts its
// yields and fails, naming the count, when they pass a bound.
func settleGoroutines(t *testing.T, want int) {
	t.Helper()
	for i := 0; runtime.NumGoroutine() > want; i++ {
		if i == 1_000_000 {
			t.Fatalf("%d goroutines are live after a clean run, want at most %d", runtime.NumGoroutine(), want)
		}
		runtime.Gosched()
	}
}
