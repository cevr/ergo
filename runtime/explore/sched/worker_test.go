package sched

import (
	"io"
	"math/rand/v2"
	"slices"
	"sync"
	"testing"
)

// memWorker is a worker in process: Serve on pipes, over a value
// executor. It counts its runs and notes a run served after an
// abandoned one.
type memWorker struct {
	toR, fromR     *io.PipeReader
	toW, fromW     *io.PipeWriter
	done           chan struct{}
	runs           int
	afterAbandoned bool
}

func (w *memWorker) Read(p []byte) (int, error)  { return w.fromR.Read(p) }
func (w *memWorker) Write(p []byte) (int, error) { return w.toW.Write(p) }

// Close ends the worker and waits until its loop has returned.
func (w *memWorker) Close() error {
	w.toW.Close()
	w.fromR.Close()
	<-w.done
	return nil
}

func startMem(x Executor, workers *[]*memWorker, mu *sync.Mutex) func() (Conn, error) {
	return func() (Conn, error) {
		w := &memWorker{done: make(chan struct{})}
		w.toR, w.toW = io.Pipe()
		w.fromR, w.fromW = io.Pipe()
		mu.Lock()
		*workers = append(*workers, w)
		mu.Unlock()
		go func() {
			defer close(w.done)
			defer w.fromW.Close()
			defer w.toR.Close()
			abandoned := false
			_ = Serve(w.toR, w.fromW, func(p Picker) Outcome {
				w.runs++
				if abandoned {
					w.afterAbandoned = true
				}
				o := x.Run(p)
				abandoned = abandoned || o.Abandoned
				return o
			})
		}()
		return w, nil
	}
}

// TestPoolReplacesAbandonedWorkers is the worker transport's guard (the
// X6 executor decision): a search through a Pool of workers sees the
// same runs as one on the executor in process; a worker serves no run
// after one it abandoned (a deadlock or a stopped replay), and the pool
// starts at most one worker more than the runs that were abandoned, so a
// worker that ran clean runs is reused.
func TestPoolReplacesAbandonedWorkers(t *testing.T) {
	for seed := uint64(1); seed <= 40; seed++ {
		r := rand.New(rand.NewPCG(seed, 17))
		x := vexec{genProg(r, 3)}
		if all, _ := naive(x, -1, naiveLimit); all == nil {
			continue
		}
		var direct, pooled []string
		DFS(x, Options{Preempt: -1, All: true, Visit: func(run Run) { direct = append(direct, Token(run.Schedule())+" "+run.Outcome.Value) }})
		var workers []*memWorker
		var mu sync.Mutex
		pool := &Pool{Start: startMem(x, &workers, &mu)}
		DFS(pool, Options{Preempt: -1, All: true, Visit: func(run Run) { pooled = append(pooled, Token(run.Schedule())+" "+run.Outcome.Value) }})
		// A replay that leaves its schedule stops the run: abandoned too.
		if stopped := Once(pool, Replay{Schedule: []string{"0.9"}}); stopped.Outcome.Stopped == "" {
			t.Fatalf("seed %d: a replay of a task that never runs was not stopped: %+v", seed, stopped.Outcome)
		}
		Once(pool, Cooperative{})
		pool.Close()
		if !slices.Equal(direct, pooled) {
			t.Fatalf("seed %d: the pool's runs differ from the executor's\nprogram: %s\ndirect %q\npooled %q", seed, x.p, direct, pooled)
		}
		if pool.Started > pool.Abandoned+1 {
			t.Fatalf("seed %d: %d workers started for %d abandoned runs", seed, pool.Started, pool.Abandoned)
		}
		for i, w := range workers {
			if w.afterAbandoned {
				t.Fatalf("seed %d: worker %d served a run after one it abandoned", seed, i)
			}
		}
	}
}
