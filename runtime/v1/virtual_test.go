package v1

import (
	"fmt"
	"math/rand/v2"
	"runtime"
	"sync"
	"testing"
	"time"

	"ergo.local/prototype/internal/lawledger"
	"ergo.local/prototype/runtime/rt"
)

// The Go runners of std testing (docs/spec/stdlib.md, Testing; batch-12
// C1): the virtual clock against a reference discrete-event model, and the
// laws of the package.

// vop is one step of a generated task: a sleep, a send to another task's
// inbox, a receive from its own inbox, a run of scheduler yields, or a
// timeout.After whose body sleeps e and whose deadline is d (d != e, so the
// race has one winner).
type vop struct {
	kind int
	d, e int64 // seconds
	to   int
}

const (
	vSleep = iota
	vSend
	vRecv
	vSpin
	vAfter
	// vStop starts a child that sleeps 100s, yields so the child parks,
	// and stops it; the step's stamp is the clock the child reads in its
	// exit, which must be the stop's instant.
	vStop
)

// vstamp is when a step ended: the clock's time in seconds since the start,
// the number of times the clock had moved on its own, and for an After
// whether it expired.
type vstamp struct {
	sec     int64
	steps   int
	expired bool
}

// vkey names a step: task i (-1 for the root's joins) and its index.
type vkey struct{ task, step int }

// TestVirtualClockModel is the model check of testing.VirtualClock (the
// red acceptance check of C1). Generated programs of 1 to 5 tasks below a
// root that made the clock: sleeps, sends and receives over queues,
// timeout.After races and runs of yields; the root then waits for each
// task in turn. A reference discrete-event model runs the same program:
// every step is instant, and the time moves only when no task can run, to
// the earliest deadline, waking every sleeper of it. Each step of the real
// run must end at the model's time and after the model's count of moves.
// So the clock moved neither while a task could run (a step would end
// late), nor past an earlier deadline, nor woke part of a group of equal
// deadlines (the count of moves would differ). A lost wake-up hangs the
// run, which the test's timeout reports. 300 seeds; a failure prints the
// seed and the program.
func TestVirtualClockModel(t *testing.T) {
	before := runtime.NumGoroutine()
	for seed := uint64(0); seed < 300; seed++ {
		prog, want := virtualProgram(seed)
		got := virtualRun(prog)
		if diff := virtualDiff(want, got); diff != "" {
			t.Fatalf("seed %d: %s\nprogram %v", seed, diff, prog)
		}
	}
	waitFor(t, "goroutines to end", func() bool { return runtime.NumGoroutine() <= before })
}

// virtualProgram draws a program that the model runs to its end, and the
// model's stamps. A program that deadlocks in the model (a receive no send
// reaches) is drawn again.
func virtualProgram(seed uint64) ([][]vop, map[vkey]vstamp) {
	r := rand.New(rand.NewPCG(seed, 12))
	for {
		n := 1 + r.IntN(5)
		prog := make([][]vop, n)
		for i := range prog {
			for range 1 + r.IntN(6) {
				o := vop{kind: r.IntN(6), d: 1 + r.Int64N(4), to: r.IntN(n)}
				if o.kind == vSend && n == 1 {
					o.kind = vSpin
				}
				if o.kind == vSend && o.to == i {
					o.to = (i + 1) % n
				}
				if o.kind == vAfter {
					o.e = 1 + r.Int64N(4)
					if o.e == o.d {
						o.e++
					}
				}
				prog[i] = append(prog[i], o)
			}
		}
		if want, ok := virtualModel(prog); ok {
			return prog, want
		}
	}
}

// virtualModel runs prog as a discrete-event model. ok is false when it
// deadlocks.
func virtualModel(prog [][]vop) (map[vkey]vstamp, bool) {
	const (
		ready = iota
		asleep
		receiving
		ended
	)
	n := len(prog)
	pc, state, until := make([]int, n), make([]int, n), make([]int64, n)
	inbox := make([]int, n)
	var now int64
	steps, joined := 0, 0
	out := map[vkey]vstamp{}
	stamp := func(i int, expired bool) {
		out[vkey{i, pc[i]}] = vstamp{now, steps, expired}
		pc[i]++
	}
	for {
		for progressed := true; progressed; {
			progressed = false
			for i := range n {
				if state[i] == receiving && inbox[i] > 0 {
					inbox[i]--
					stamp(i, false)
					state[i], progressed = ready, true
				}
				for state[i] == ready && pc[i] < len(prog[i]) {
					o := prog[i][pc[i]]
					switch o.kind {
					case vSleep:
						state[i], until[i] = asleep, now+o.d
					case vAfter:
						state[i], until[i] = asleep, now+min(o.d, o.e)
					case vSend:
						inbox[o.to]++
						stamp(i, false)
					case vRecv:
						if inbox[i] > 0 {
							inbox[i]--
							stamp(i, false)
						} else {
							state[i] = receiving
						}
					case vSpin, vStop:
						stamp(i, false)
					}
					progressed = true
				}
				if state[i] == ready && pc[i] == len(prog[i]) {
					state[i], progressed = ended, true
				}
			}
			for joined < n && state[joined] == ended {
				out[vkey{-1, joined}] = vstamp{now, steps, false}
				joined++
				progressed = true
			}
		}
		if joined == n {
			return out, true
		}
		next := int64(-1)
		for i := range n {
			if state[i] == asleep && (next < 0 || until[i] < next) {
				next = until[i]
			}
		}
		if next < 0 {
			return nil, false
		}
		now, steps = next, steps+1
		for i := range n {
			if state[i] == asleep && until[i] == now {
				o := prog[i][pc[i]]
				stamp(i, o.kind == vAfter && o.d < o.e)
				state[i] = ready
			}
		}
	}
}

// lineSleeper is a Sleeper over a Timeline, the Scheduler a program binds.
type lineSleeper struct{ l *Timeline }

func (s lineSleeper) Sleep(t *rt.Task, d time.Duration) { s.l.Wait(t, d) }

// virtualRun runs prog on tasks below a root that made a VirtualClock's
// timeline, and returns the stamp of each step.
func virtualRun(prog [][]vop) map[vkey]vstamp {
	root := rt.Root("model")
	line := NewVirtualTimeline(root, 0, 0)
	inbox := make([]*Queue[int], len(prog))
	for i := range inbox {
		inbox[i] = New[int](64)
	}
	var mu sync.Mutex
	out := map[vkey]vstamp{}
	stamp := func(i, k int, expired bool) {
		r := line.Read()
		steps := line.v.Steps()
		mu.Lock()
		out[vkey{i, k}] = vstamp{r.Mono / int64(time.Second), steps, expired}
		mu.Unlock()
	}
	s := NewScope(root)
	defer s.End(nil)
	tasks := make([]*Task[struct{}], len(prog))
	for i, ops := range prog {
		tasks[i] = ownedGo(&s, Site(fmt.Sprint("task ", i)), func(ct *rt.Task) (struct{}, error) {
			for k, o := range ops {
				switch o.kind {
				case vSleep:
					line.Wait(ct, time.Duration(o.d)*time.Second)
					stamp(i, k, false)
				case vAfter:
					_, err := After("after", ct, lineSleeper{line}, time.Duration(o.d)*time.Second, func(bt *rt.Task) (int, error) {
						line.Wait(bt, time.Duration(o.e)*time.Second)
						return 0, nil
					})
					stamp(i, k, err == Expired{})
				case vSend:
					if err := inbox[o.to].Push(ct, k); err != nil {
						return struct{}{}, err
					}
					stamp(i, k, false)
				case vRecv:
					if _, err := inbox[i].Pop(ct); err != nil {
						return struct{}{}, err
					}
					stamp(i, k, false)
				case vSpin:
					for range 3 {
						runtime.Gosched()
					}
					stamp(i, k, false)
				case vStop:
					ls := NewScope(ct)
					h := ownedGo(&ls, "sleeper", func(st *rt.Task) (struct{}, error) {
						defer stamp(i, k, false)
						line.Wait(st, 100*time.Second)
						return struct{}{}, nil
					})
					for range 3 {
						runtime.Gosched()
					}
					h.Stop()
					ls.End(nil)
				}
			}
			return struct{}{}, nil
		})
	}
	for i, h := range tasks {
		if _, err := h.Wait(); err != nil {
			panic(err)
		}
		stamp(-1, i, false)
	}
	return out
}

// virtualDiff names the first step whose stamp differs, or "".
func virtualDiff(want, got map[vkey]vstamp) string {
	for k, w := range want {
		g, ok := got[k]
		if !ok {
			return fmt.Sprintf("step %v did not run", k)
		}
		if g != w {
			return fmt.Sprintf("step %v ended at %+v, model %+v", k, g, w)
		}
	}
	if len(got) != len(want) {
		return fmt.Sprintf("%d steps ran, model %d", len(got), len(want))
	}
	return ""
}

// TestTestingLaws runs the laws of std testing against VirtualClock's
// timeline (docs/spec/stdlib.md, Std laws).
func TestTestingLaws(t *testing.T) {
	span := func(n int64) time.Duration {
		return time.Duration(1 + (n%1099511627775+1099511627775)%1099511627775)
	}
	lawledger.Run(t, map[string]lawledger.Law{
		"testing SleepAdvancesExactly": {Statement: `SleepAdvancesExactly(n int64) { let c = NewVirtualClock(0) let d = span(n) with context { Scheduler: c, time.Clock: c } { let before = time.Clock.Now() Scheduler.Sleep(d) time.Clock.Now().Sub(before) == d } }`, Check: func(t *testing.T) {
			r := rand.New(rand.NewPCG(1, 13))
			for range 200 {
				root := rt.Root("law")
				line := NewVirtualTimeline(root, 0, 0)
				d := span(r.Int64())
				before := line.Read()
				line.Wait(root, d)
				if got := line.Read().Mono - before.Mono; got != int64(d) {
					t.Fatalf("Sleep(%v) moved the clock by %v", d, time.Duration(got))
				}
			}
		}},
		"testing IdleOnlyAdvance": {Statement: `IdleOnlyAdvance(n int64, k int) { let c = NewVirtualClock(0) let d = span(n) let count = 1 + (k%50+50)%50 with context { Scheduler: c, time.Clock: c } { let start = time.Clock.Now() let q = queue.New[int](count) let t = task.Go() { for i in 0..count { try q.Push(i) } time.Clock.Now() } Scheduler.Sleep(d) let seen = t.Wait() catch { case queue.Closed: time.Unix(1, 0) } seen.Equal(start) && time.Clock.Now().Sub(start) == d && q.Len() == count } }`, Check: func(t *testing.T) {
			r := rand.New(rand.NewPCG(1, 14))
			for range 200 {
				root := rt.Root("law")
				line := NewVirtualTimeline(root, 0, 0)
				d := span(r.Int64())
				count := 1 + r.IntN(50)
				q := New[int](count)
				s := NewScope(root)
				h := ownedGo(&s, "pusher", func(ct *rt.Task) (int64, error) {
					for i := range count {
						if err := q.Push(ct, i); err != nil {
							return 0, err
						}
						runtime.Gosched()
					}
					return line.Read().Mono, nil
				})
				line.Wait(root, d)
				seen, err := h.Wait()
				s.End(nil)
				if err != nil || seen != 0 || line.Read().Mono != int64(d) || q.Len() != count {
					t.Fatalf("pusher saw %v (err %v), clock %v after Sleep(%v), %d queued of %d", time.Duration(seen), err, time.Duration(line.Read().Mono), d, q.Len(), count)
				}
			}
		}},
	})
}
