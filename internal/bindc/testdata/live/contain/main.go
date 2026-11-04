// Command contain drives the panic containment and the Owner of the fixture
// library through the generated typed layer, for TestLivePanicContainment
// and TestLiveOwnerModel. Modes:
//
//	contain <lib>           the containment checks on one locked thread: one
//	                        "ok: <check>" line each;
//	stress <lib> <n>        100 goroutines (GOMAXPROCS at least 4) that do
//	                        not lock their threads make n calls each whose
//	                        callbacks call runtime.Gosched and may panic;
//	goexit <lib>            a callback calls runtime.Goexit: the process must
//	                        end with exit status 2 before C frames unwind;
//	goexit-owner <lib>      a function of Owner.Do calls runtime.Goexit;
//	owner <lib> <seed> <n>  n generated schedules of the Owner (owner.qnt).
//
// It exits 1 at the first failed check. The proof that C frames completed
// is the fixture's own counters (fx_context_counters), not Go-side flags.
package main

import (
	"bytes"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"runtime"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"ergo.local/bind/fixture"
)

func fail(format string, args ...any) {
	fmt.Printf("FAIL: "+format+"\n", args...)
	os.Exit(1)
}

func ok(check string) { fmt.Println("ok: " + check) }

func must(what string, err error) {
	if err != nil {
		fail("%s: %v", what, err)
	}
}

// catch runs f and returns the value it panicked with, or nil.
func catch(f func()) (v any) {
	defer func() { v = recover() }()
	f()
	return nil
}

// wantPanic fails unless v is a *fixture.CallbackPanic of typedef whose
// value is value, with a stack.
func wantPanic(what string, v any, typedef string, value any) *fixture.CallbackPanic {
	p, isCP := v.(*fixture.CallbackPanic)
	if !isCP {
		fail("%s: recovered %T %v, want a *fixture.CallbackPanic", what, v, v)
	}
	if p.Typedef != typedef || p.Value != value {
		fail("%s: CallbackPanic{%s, %v}, want {%s, %v}", what, p.Typedef, p.Value, typedef, value)
	}
	if !bytes.Contains(p.Stack, []byte("main.")) {
		fail("%s: the stack does not show the callback:\n%s", what, p.Stack)
	}
	return p
}

func main() {
	if len(os.Args) < 3 {
		fail("usage: contain contain|stress|goexit|goexit-owner|owner <lib> ...")
	}
	lib := os.Args[2]
	must("Load", fixture.Load(lib))
	arg := func(i int) int {
		if len(os.Args) <= i {
			fail("mode %s: argument %d is missing", os.Args[1], i)
		}
		n, err := strconv.Atoi(os.Args[i])
		if err != nil {
			fail("argument %d: %v", i, err)
		}
		return n
	}
	switch os.Args[1] {
	case "contain":
		// A context belongs to the OS thread that creates it.
		runtime.LockOSThread()
		contain()
	case "stress":
		stress(arg(3))
	case "goexit":
		goexit()
	case "goexit-owner":
		goexitOwner()
	case "owner":
		owner(uint64(arg(3)), arg(4))
	default:
		fail("unknown mode %s", os.Args[1])
	}
}

// counts is the change of a context's counters over f.
func counts(ctx *fixture.Context, f func()) fixture.Counters {
	before, err := fixture.ContextCounters(ctx)
	must("ContextCounters", err)
	f()
	after, err := fixture.ContextCounters(ctx)
	must("ContextCounters", err)
	return fixture.Counters{
		Started:      after.Started - before.Started,
		Returned:     after.Returned - before.Returned,
		WriteResults: after.WriteResults - before.WriteResults,
		Unwritten:    after.Unwritten - before.Unwritten,
	}
}

// clean fails unless a typed call on this thread runs every callback and
// returns normally: no stale panic is pending.
func clean(what string, ctx *fixture.Context) {
	runs := 0
	var taken int64
	var err error
	if v := catch(func() {
		taken, err = fixture.ContextRepeat(ctx, 3, func([]byte) int64 { runs++; return 2 })
	}); v != nil || err != nil || taken != 6 || runs != 3 {
		fail("%s: the next typed call: panic %v, %d %v after %d runs; want 6 after 3 runs", what, v, taken, err, runs)
	}
}

func contain() {
	ctx, err := fixture.ContextCreate(fixture.ContextOptions{NodeCapacity: 8})
	must("ContextCreate", err)

	// A call-scoped callback panics at its 2nd of 5 invocations.
	live := fixture.LiveCallbacks()
	var ran []int
	var v any
	d := counts(ctx, func() {
		v = catch(func() {
			_, _ = fixture.ContextRepeat(ctx, 5, func([]byte) int64 {
				ran = append(ran, len(ran)+1)
				if len(ran) == 2 {
					panic("write boom")
				}
				return 1
			})
			fail("ContextRepeat returned; want the panic of its callback")
		})
	})
	if d.Started != 5 || d.Returned != 5 {
		fail("call scope: C counters started %d returned %d, want 5 5 (C frames completed); recovered %v", d.Started, d.Returned, v)
	}
	wantPanic("call scope", v, "fx_write_callback", "write boom")
	ok("call-scoped panic: C frames completed, the typed call re-raised it")
	// C got 1, then the default -1 for the panic and for the 3 invocations
	// after it, which ran no Go code.
	if !slices.Equal(ran, []int{1, 2}) || d.WriteResults != 1-4 {
		fail("call scope: Go ran %v and C got a sum of %d; want [1 2] and -3", ran, d.WriteResults)
	}
	ok("no Go code ran after the panic, and C got the default")
	if fixture.LiveCallbacks() != live {
		fail("call scope: LiveCallbacks %d after the panic, want %d", fixture.LiveCallbacks(), live)
	}
	clean("call scope", ctx)

	// A registered callback panics: node b of a, b, c.
	var log []string
	armed := false
	measure := func(name string, x float32) fixture.MeasureCallback {
		return func(float32, uint32, float32, uint32) [2]float32 {
			log = append(log, name)
			if armed && name == "b" {
				panic("measure boom")
			}
			return [2]float32{x, x}
		}
	}
	var nodes []fixture.Node
	for i, name := range []string{"a", "b", "c"} {
		nd, err := fixture.NodeCreate(ctx, uint32(i))
		must("NodeCreate", err)
		must("NodeSetMeasure "+name, fixture.NodeSetMeasure(ctx, nd, measure(name, float32(i+1))))
		nodes = append(nodes, nd)
	}
	live = fixture.LiveCallbacks()
	armed, log = true, nil
	d = counts(ctx, func() {
		v = catch(func() {
			_, _ = fixture.ContextMeasureAll(ctx, 0, 0)
			fail("ContextMeasureAll returned; want the panic of b")
		})
	})
	if d.Started != 3 || d.Returned != 3 {
		fail("registered: C counters started %d returned %d, want 3 3 (C frames completed); recovered %v", d.Started, d.Returned, v)
	}
	wantPanic("registered", v, "fx_measure_callback", "measure boom")
	// b panicked and c ran no Go code: C found both out arrays unwritten.
	if !slices.Equal(log, []string{"a", "b"}) || d.Unwritten != 2 {
		fail("registered: Go ran %v and C found %d out arrays unwritten; want [a b] and 2", log, d.Unwritten)
	}
	if p, err := fixture.NodeMeasure(ctx, nodes[0], 0, 0); err != nil || p != (fixture.Point{X: 1, Y: 1}) || fixture.LiveCallbacks() != live {
		fail("registered: after the panic NodeMeasure a is %+v %v, live %d; want {1 1} and %d", p, err, fixture.LiveCallbacks(), live)
	}
	ok("registered panic: C frames completed, the typed call re-raised it, the registry is intact")
	armed = false
	clean("registered", ctx)

	// A panic in the set probe: C installed the callback, so the entry
	// stays; a panic in the clear probe: C cleared it, so the entry goes.
	nd, err := fixture.NodeCreate(ctx, 7)
	must("NodeCreate d", err)
	live = fixture.LiveCallbacks()
	v = catch(func() {
		_ = fixture.NodeSetMeasure(ctx, nd, func(float32, uint32, float32, uint32) [2]float32 { panic("probe boom") })
	})
	wantPanic("set probe", v, "fx_measure_callback", "probe boom")
	if fixture.LiveCallbacks() != live+1 {
		fail("set probe: LiveCallbacks %d after the panic, want %d (C installed it)", fixture.LiveCallbacks(), live+1)
	}
	v = catch(func() { _ = fixture.NodeSetMeasure(ctx, nd, nil) })
	wantPanic("clear probe", v, "fx_measure_callback", "probe boom")
	if fixture.LiveCallbacks() != live {
		fail("clear probe: LiveCallbacks %d after the panic, want %d (C cleared it)", fixture.LiveCallbacks(), live)
	}
	if _, err := fixture.NodeMeasure(ctx, nd, 0, 0); !isStatus(err, fixture.StatusInvalidArgument) {
		fail("clear probe: NodeMeasure after the clear: %v, want StatusInvalidArgument (C has no callback)", err)
	}
	ok("a panic in a set or a clear probe: the registry follows what C did")

	// The values: an error unwraps, other values do not; panic(nil) is a
	// *runtime.PanicNilError; a *CallbackPanic is kept, not wrapped again.
	boom := errors.New("boom")
	panicWith := func(x any) any {
		return catch(func() {
			_, _ = fixture.ContextRepeat(ctx, 1, func([]byte) int64 { panic(x) })
		})
	}
	p := wantPanic("error value", panicWith(boom), "fx_write_callback", boom)
	if !errors.Is(p, boom) || errors.Unwrap(p) != boom {
		fail("error value: errors.Is %v, Unwrap %v; want true and the error", errors.Is(p, boom), errors.Unwrap(p))
	}
	if p := wantPanic("string value", panicWith("text"), "fx_write_callback", "text"); p.Unwrap() != nil {
		fail("string value: Unwrap %v, want nil", p.Unwrap())
	}
	cp, _ := panicWith(nil).(*fixture.CallbackPanic)
	var pne *runtime.PanicNilError
	if cp == nil || !errors.As(cp, &pne) {
		fail("panic(nil): recovered %+v, want a CallbackPanic of a *runtime.PanicNilError", cp)
	}
	mine := &fixture.CallbackPanic{Typedef: "mine", Value: 1}
	if got := panicWith(mine); got != mine {
		fail("a *CallbackPanic value: recovered %+v, want the same pointer", got)
	}
	ok("the panic value: errors unwrap, panic(nil) is a PanicNilError, a CallbackPanic is kept")
	clean("values", ctx)

	// Nesting: callback -> typed call -> callback panics. The inner typed
	// call re-raises it in the outer callback, whose trampoline contains it
	// again (the same *CallbackPanic); the outer typed call re-raises it.
	ran = nil
	d = counts(ctx, func() {
		v = catch(func() {
			_, _ = fixture.ContextRepeat(ctx, 3, func([]byte) int64 {
				ran = append(ran, 10+len(ran))
				_, _ = fixture.ContextRepeat(ctx, 4, func([]byte) int64 {
					ran = append(ran, 20+len(ran))
					if len(ran) == 3 {
						panic("inner boom")
					}
					return 1
				})
				fail("the inner ContextRepeat returned; want the panic of its callback")
				return 1
			})
			fail("the outer ContextRepeat returned; want the inner panic")
		})
	})
	wantPanic("nested", v, "fx_write_callback", "inner boom")
	// Inner: 1, then -1 for the panic and the 2 after it; outer: -1 for the
	// invocation that panicked and for the 2 after it.
	if d.Started != 7 || d.Returned != 7 || !slices.Equal(ran, []int{10, 21, 22}) || d.WriteResults != (1-3)+(-3) {
		fail("nested: counters %+v, Go ran %v; want started 7 returned 7, a sum of -5 and [10 21 22]", d, ran)
	}
	// The same in a registered callback: measure -> repeat -> writer panics.
	nested := nodes[1]
	must("NodeSetMeasure nested", fixture.NodeSetMeasure(ctx, nested, nil))
	must("NodeSetMeasure nested", fixture.NodeSetMeasure(ctx, nested, func(float32, uint32, float32, uint32) [2]float32 {
		if armed {
			_, _ = fixture.ContextRepeat(ctx, 2, func([]byte) int64 { panic("deep boom") })
		}
		return [2]float32{9, 9}
	}))
	armed = true
	d = counts(ctx, func() {
		v = catch(func() {
			_, _ = fixture.NodeMeasure(ctx, nested, 0, 0)
			fail("NodeMeasure returned; want the inner panic")
		})
	})
	armed = false
	wantPanic("nested in registered", v, "fx_write_callback", "deep boom")
	if d.Started != 3 || d.Returned != 3 || d.Unwritten != 1 || d.WriteResults != -2 {
		fail("nested in registered: counters %+v; want started 3 returned 3, 1 unwritten, a sum of -2", d)
	}
	ok("nested panic surfaces at the outermost typed call; C counters match at both levels")
	clean("nested", ctx)

	// An outer callback that recovers the inner typed call's panic leaves
	// nothing pending: its later invocations run Go code.
	outer := 0
	var inner any
	taken, err := fixture.ContextRepeat(ctx, 3, func([]byte) int64 {
		outer++
		if outer == 1 {
			inner = catch(func() {
				_, _ = fixture.ContextRepeat(ctx, 2, func([]byte) int64 { panic("caught boom") })
			})
		}
		return 1
	})
	wantPanic("recovered inner", inner, "fx_write_callback", "caught boom")
	if err != nil || taken != 3 || outer != 3 {
		fail("recovered inner: the outer call took %d %v after %d runs, want 3 after 3", taken, err, outer)
	}
	ok("a panic the outer callback recovers leaves nothing pending")
	clean("end", ctx)
	ok("the next typed call on the thread is clean")
	must("ContextDestroy", fixture.ContextDestroy(ctx))
}

func isStatus(err error, want fixture.Status) bool {
	var st fixture.Status
	return errors.As(err, &st) && st == want
}

// stress: 100 goroutines that do not lock their threads. Each makes n
// calls of ContextRepeat on a context of its own; its callbacks call
// runtime.Gosched, and two calls of three panic with a value that names
// the goroutine and the call. Each call must re-raise exactly its own
// panic, or return its own sum. A goroutine that moves to another thread
// between ContextCreate and ContextRepeat gets the wrong-thread status (no
// callback runs); it tries again with a new context.
func stress(n int) {
	runtime.GOMAXPROCS(max(4, runtime.GOMAXPROCS(0)))
	var raised, returned, moved atomic.Int64
	var wg sync.WaitGroup
	for g := range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for it := 0; it < n; it++ {
				ctx, err := fixture.ContextCreate(fixture.ContextOptions{NodeCapacity: 1})
				must("ContextCreate", err)
				want := fmt.Sprintf("g%d-%d", g, it)
				at := it % 3 // 0: no panic; else the invocation that panics
				runs := 0
				var taken int64
				v := catch(func() {
					taken, err = fixture.ContextRepeat(ctx, 4, func([]byte) int64 {
						runs++
						runtime.Gosched()
						if runs == at {
							panic(want)
						}
						return 1
					})
				})
				switch {
				case v == nil && isStatus(err, fixture.StatusWrongThread) && runs == 0:
					moved.Add(1)
					it--
					continue // the context is lost with its thread
				case v != nil:
					cp, isCP := v.(*fixture.CallbackPanic)
					if !isCP || cp.Value != want || at == 0 {
						fail("goroutine %d call %d: recovered %v, want %s", g, it, v, map[bool]string{true: "no panic", false: want}[at == 0])
					}
					raised.Add(1)
				default:
					if at != 0 || err != nil || taken != 4 || runs != 4 {
						fail("goroutine %d call %d: returned %d %v after %d runs; want %s", g, it, taken, err, runs,
							map[bool]string{true: "4 after 4 runs", false: "the panic " + want}[at == 0])
					}
					returned.Add(1)
				}
				_ = fixture.ContextDestroy(ctx) // the wrong-thread status when it moved
			}
		}()
	}
	wg.Wait()
	fmt.Printf("stress: GOMAXPROCS %d, %d raised, %d returned, %d moved between calls\n", runtime.GOMAXPROCS(0), raised.Load(), returned.Load(), moved.Load())
	ok("no goroutine got another goroutine's panic or lost its own")
}

// goexit: a callback calls runtime.Goexit. The trampoline must end the
// process with exit status 2 before the C frames unwind, so the deferred
// close below never runs.
func goexit() {
	unwound := make(chan struct{})
	go func() {
		defer close(unwound)
		runtime.LockOSThread()
		ctx, err := fixture.ContextCreate(fixture.ContextOptions{NodeCapacity: 1})
		must("ContextCreate", err)
		_, _ = fixture.ContextRepeat(ctx, 3, func([]byte) int64 {
			fmt.Println("the callback calls runtime.Goexit")
			runtime.Goexit()
			return 1
		})
	}()
	select {
	case <-unwound:
		fail("runtime.Goexit unwound the C frames")
	case <-time.After(20 * time.Second):
		fail("no exit after runtime.Goexit")
	}
}

// goexitOwner: a function of Owner.Do calls runtime.Goexit; the process
// must end with exit status 2 (the runner would stop, and Do would wait).
func goexitOwner() {
	o := fixture.NewOwner()
	done := make(chan error)
	go func() { done <- o.Do(func() { runtime.Goexit() }) }()
	select {
	case err := <-done:
		fail("Do returned %v after runtime.Goexit", err)
	case <-time.After(20 * time.Second):
		fail("no exit after runtime.Goexit in Do")
	}
}

// The Owner schedules (TestLiveOwnerModel, owner.qnt).
type opKind int

const (
	opPlain        opKind = iota // the job uses the context and returns
	opPanic                      // the job panics
	opReDo                       // the job calls Do: it runs inline
	opReCallback                 // a callback inside the job calls Do: inline
	opRePanic                    // an inline Do panics in the job
	opCloseOnOwner               // the job calls Close: ErrOwnerThread
)

var opNames = []string{"plain", "panic", "re-entrant Do", "Do in a callback", "inline panic", "Close on the owner"}

func pickOp(r *rand.Rand) opKind {
	switch x := r.IntN(20); {
	case x < 8:
		return opPlain
	case x < 12:
		return opPanic
	case x < 15:
		return opReDo
	case x < 18:
		return opReCallback
	case x < 19:
		return opRePanic
	}
	return opCloseOnOwner
}

// deadline is the most a Do or a Close may take; past it the driver
// fails: a Do that waits on a stopped runner or on itself never returns.
const deadline = 10 * time.Second

func withDeadline(what string, f func()) {
	t := time.AfterFunc(deadline, func() { fail("%s did not return within %v (stranded, or waiting on itself)", what, deadline) })
	defer t.Stop()
	f()
}

type schedule struct {
	o              *fixture.Owner
	ctx            *fixture.Context
	thread         uint64
	started, ended atomic.Int64
	closeBegun     atomic.Bool
	closeReturned  atomic.Bool
	mu             sync.Mutex
	violations     []string
}

func (s *schedule) violate(format string, args ...any) {
	s.mu.Lock()
	s.violations = append(s.violations, fmt.Sprintf(format, args...))
	s.mu.Unlock()
}

// onOwner checks that the calling code runs on the owner thread and that a
// fixture call on the owner's context does not get the wrong-thread status.
func (s *schedule) onOwner(what string) {
	if tid, _ := fixture.ThreadId(); tid != s.thread {
		s.violate("%s ran on thread %d, the owner thread is %d", what, tid, s.thread)
	}
	if _, err := fixture.NodeCount(s.ctx); err != nil {
		s.violate("%s: NodeCount on the owner's context: %v", what, err)
	}
}

// job is one Do of a schedule.
type job struct {
	kind         opKind
	value        string
	pause        time.Duration
	began, ended bool
}

func (s *schedule) run(j *job) {
	if s.closeReturned.Load() {
		s.violate("noStartAfterClose: job %s started after Close returned", j.value)
	}
	j.began = true
	s.started.Add(1)
	defer s.ended.Add(1)
	s.onOwner("job " + j.value)
	time.Sleep(j.pause)
	switch j.kind {
	case opPanic:
		panic(j.value)
	case opReDo:
		inner := false
		var err error
		withDeadline("a re-entrant Do", func() { err = s.o.Do(func() { s.onOwner("an inline Do"); inner = true }) })
		if err != nil || !inner {
			s.violate("a re-entrant Do: %v, ran %v; want nil and inline", err, inner)
		}
	case opReCallback:
		inner := 0
		var taken int64
		var err error
		withDeadline("a Do in a callback", func() {
			taken, err = fixture.ContextRepeat(s.ctx, 2, func([]byte) int64 {
				if err := s.o.Do(func() { s.onOwner("a Do in a callback"); inner++ }); err != nil {
					s.violate("a Do in a callback: %v", err)
				}
				return 1
			})
		})
		if err != nil || taken != 2 || inner != 2 {
			s.violate("a Do in a callback: %d %v, %d inline runs; want 2 nil 2", taken, err, inner)
		}
	case opRePanic:
		v := catch(func() { _ = s.o.Do(func() { panic(j.value + "-inline") }) })
		if v != j.value+"-inline" {
			s.violate("an inline Do that panics: the job recovered %v, want %s-inline", v, j.value)
		}
	case opCloseOnOwner:
		if err := s.o.Close(); err != fixture.ErrOwnerThread {
			s.violate("Close on the owner thread: %v, want ErrOwnerThread", err)
		}
	}
	j.ended = true
}

func owner(seed uint64, n int) {
	r := rand.New(rand.NewPCG(seed, 0x0e2c))
	stats := map[string]int{}
	for i := range n {
		ownerSchedule(r, i, stats)
	}
	fmt.Printf("owner: %d schedules, outcomes %v\n", n, stats)
	for _, k := range []string{"nil", "panic", "closed", "Close found a job running", "late Do",
		"op re-entrant Do", "op Do in a callback", "op inline panic", "op Close on the owner"} {
		if stats[k] == 0 {
			fail("the schedules made no case of %q", k)
		}
	}
	ok("noStranded: every Do returned nil, its panic, or ErrOwnerClosed")
	ok("noStartAfterClose: no job started after Close returned, and Close returned after the runner stopped")
	ok("noSelfWait: a Do on the owner thread ran inline")
	ok("every job ran on the owner thread, and no fixture call got the wrong-thread status")
}

func ownerSchedule(r *rand.Rand, i int, stats map[string]int) {
	s := &schedule{o: fixture.NewOwner()}
	withDeadline("the first Do", func() {
		must("the first Do", s.o.Do(func() {
			tid, err := fixture.ThreadId()
			must("ThreadId", err)
			s.thread = tid
			s.ctx, err = fixture.ContextCreate(fixture.ContextOptions{NodeCapacity: 2})
			must("ContextCreate on the owner", err)
		}))
	})
	ng := 1 + r.IntN(8)
	plans := make([][]*job, ng)
	total := 0
	for g := range plans {
		for k := range 1 + r.IntN(6) {
			kind := pickOp(r)
			plans[g] = append(plans[g], &job{kind: kind, value: fmt.Sprintf("s%d-g%d-%d", i, g, k), pause: time.Duration(r.IntN(300)) * time.Microsecond})
			stats["op "+opNames[kind]]++
			total++
		}
	}
	closeAt := r.IntN(total + 1) // Close once closeAt Do calls have begun
	var begun atomic.Int64
	var wg sync.WaitGroup
	var outMu sync.Mutex
	outcome := func(k string) {
		outMu.Lock()
		stats[k]++
		outMu.Unlock()
	}
	for g := range plans {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _, j := range plans[g] {
				begun.Add(1)
				late := s.closeReturned.Load()
				var err error
				var v any
				withDeadline("Do of "+j.value, func() { v = catch(func() { err = s.o.Do(func() { s.run(j) }) }) })
				switch {
				case v != nil:
					if j.kind != opPanic || v != j.value || !j.began {
						s.violate("Do of %s (%s) panicked with %v", j.value, opNames[j.kind], v)
					}
					outcome("panic")
				case err == nil:
					if j.kind == opPanic || !j.ended {
						s.violate("Do of %s (%s) returned nil, the job began %v and ended %v", j.value, opNames[j.kind], j.began, j.ended)
					}
					outcome("nil")
				case err == fixture.ErrOwnerClosed:
					if j.began || !s.closeBegun.Load() {
						s.violate("Do of %s returned ErrOwnerClosed; the job began %v, Close began %v", j.value, j.began, s.closeBegun.Load())
					}
					outcome("closed")
				default:
					s.violate("Do of %s returned %v", j.value, err)
				}
				if late {
					outcome("late Do")
					if err != fixture.ErrOwnerClosed {
						s.violate("a Do after Close returned gave %v, want ErrOwnerClosed", err)
					}
				}
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for begun.Load() < int64(closeAt) {
			time.Sleep(20 * time.Microsecond)
		}
		s.closeBegun.Store(true)
		if s.started.Load() > s.ended.Load() {
			outcome("Close found a job running")
		}
		var err error
		withDeadline("Close", func() { err = s.o.Close() })
		if st, en := s.started.Load(), s.ended.Load(); st != en {
			s.violate("Close returned while the runner had %d jobs begun and %d ended", st, en)
		}
		s.closeReturned.Store(true)
		if err != nil {
			s.violate("Close: %v", err)
		}
	}()
	wg.Wait()
	if err := s.o.Close(); err != fixture.ErrOwnerClosed {
		s.violate("a second Close: %v, want ErrOwnerClosed", err)
	}
	if err := s.o.Do(func() { s.violate("a Do after Close ran") }); err != fixture.ErrOwnerClosed {
		s.violate("a Do after Close: %v, want ErrOwnerClosed", err)
	}
	if len(s.violations) > 0 {
		fail("schedule %d (%d goroutines, Close after %d of %d Do): %v", i, ng, closeAt, total, s.violations)
	}
}
