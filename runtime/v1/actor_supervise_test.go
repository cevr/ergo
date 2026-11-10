package v1

import (
	"errors"
	"fmt"
	"math/rand"
	"runtime"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"ergo.local/prototype/runtime/rt"
)

// A supervised actor (actor.go rule 16; the model
// docs/research/actor-machine/supervise.qnt) against a sequential
// reference: generated batches of Sends, Asks and State reads with
// defects injected at random steps, Done, typed failures, Stops and
// Drains, and fresh states that fail at random restarts. Each batch is
// queued while the runner is held in a step, so the order of the inbox
// is the batch's and the reference steps the same trace. Every wait waits
// for an event (an answer, a fresh call, the runner's end), never for a
// wall-clock bound.

// Signals of the supervised machine; a signal >= 0 is a Stay that appends
// it to the state.
const (
	supDefect = -1 // the step raises
	supDone   = -2 // Done
	supHold   = -3 // the step waits until the batch is queued; Ignore
	supFail   = -4 // the step's typed failure
)

// supOp is one envelope of a batch: a Send, an Ask or a State read.
type supOp struct {
	kind uint8 // sendSignal, askSignal or stateRead
	e    int
}

type supBatch struct {
	ops   []supOp
	drain bool // Drain while the batch is queued
	stop  bool // Stop after the batch
}

type supCase struct {
	limit      int
	freshFails map[int]bool // the restarts (1, 2, …) whose fresh state fails
	batches    []supBatch
}

// supAnswer is the answer an Ask or a State read gets.
type supAnswer struct {
	d   rdec
	s   []int
	err string
}

func (x supAnswer) String() string { return fmt.Sprintf("{%v %v %v}", x.d, x.s, x.err) }

// supBatchOut is what one batch gives: its answers, in order, the fresh
// calls it causes, and whether the actor ended in it.
type supBatchOut struct {
	answers []supAnswer
	fresh   int
	ended   bool
}

type supOutcome struct {
	batches  []supBatchOut
	exit     int
	restarts int
	result   string
}

func supDraw(seed int64) supCase {
	r := rand.New(rand.NewSource(seed))
	c := supCase{limit: r.Intn(4), freshFails: map[int]bool{}}
	for i := 1; i <= 4; i++ {
		if r.Intn(5) == 0 {
			c.freshFails[i] = true
		}
	}
	for range 1 + r.Intn(6) {
		var b supBatch
		for range r.Intn(6) {
			op := supOp{kind: sendSignal, e: r.Intn(10)}
			switch r.Intn(3) {
			case 1:
				op.kind = askSignal
			case 2:
				op.kind = stateRead
			}
			switch n := r.Intn(20); {
			case n < 4:
				op.e = supDefect
			case n == 4:
				op.e = supDone
			case n == 5:
				op.e = supFail
			}
			b.ops = append(b.ops, op)
		}
		b.drain = r.Intn(8) == 0
		b.stop = !b.drain && r.Intn(10) == 0
		c.batches = append(c.batches, b)
	}
	return c
}

// supModel is the reference: a sequential interpretation of rule 16.
func supModel(c supCase) supOutcome {
	var o supOutcome
	state := []int{}
	ended, closed := false, false
	exit := exitHalted
	result := "ok"
	end := func(kind int) {
		ended, closed, exit = true, true, kind
	}
	for _, b := range c.batches {
		var out supBatchOut
		if ended {
			// Every Send, Ask and the batch's hold fail Stopped at
			// admission; a State read gets the last state.
			for _, op := range b.ops {
				if op.kind == stateRead {
					out.answers = append(out.answers, supAnswer{s: state})
				} else if op.kind == askSignal {
					out.answers = append(out.answers, supAnswer{err: "actor.Stopped"})
				}
			}
			out.answers = append(out.answers, supAnswer{s: state})
			out.ended = true
			o.batches = append(o.batches, out)
			continue
		}
		if b.drain {
			closed = true
		}
		ops := append(slices.Clone(b.ops), supOp{kind: stateRead})
		for i := 0; i < len(ops); i++ {
			op := ops[i]
			if ended {
				// After the end: Asks Stopped, reads the last state.
				switch op.kind {
				case askSignal:
					out.answers = append(out.answers, supAnswer{err: "actor.Stopped"})
				case stateRead:
					out.answers = append(out.answers, supAnswer{s: state, err: "actor.Stopped"})
				}
				continue
			}
			if op.kind == stateRead {
				out.answers = append(out.answers, supAnswer{s: state})
				continue
			}
			asked := op.kind == askSignal
			switch {
			case op.e >= 0:
				state = append(slices.Clone(state), op.e)
				if asked {
					out.answers = append(out.answers, supAnswer{d: rdec{decideStay, op.e}})
				}
			case op.e == supDone:
				if asked {
					out.answers = append(out.answers, supAnswer{d: rdec{decideDone, op.e}})
				}
				end(exitFinished)
			case op.e == supFail:
				if asked {
					out.answers = append(out.answers, supAnswer{err: errBoom.Error()})
				} else {
					end(exitFailed)
					result = "failed"
				}
			case op.e == supDefect:
				// The crash: the step's Ask and the queued ones Stopped,
				// the queued Sends dropped, the queued reads the state.
				// An end (no restart) answers them as rule 8 does.
				stopped := ""
				if closed || o.restarts >= c.limit {
					stopped = "actor.Stopped"
				}
				if asked {
					out.answers = append(out.answers, supAnswer{err: "actor.Stopped"})
				}
				for _, q := range ops[i+1:] {
					switch q.kind {
					case askSignal:
						out.answers = append(out.answers, supAnswer{err: "actor.Stopped"})
					case stateRead:
						out.answers = append(out.answers, supAnswer{s: state, err: stopped})
					}
				}
				i = len(ops)
				for {
					if closed || o.restarts >= c.limit {
						end(exitDefected)
						result = "defect"
						break
					}
					o.restarts++
					out.fresh++
					if !c.freshFails[o.restarts] {
						state = []int{}
						break
					}
				}
			}
		}
		if b.drain && !ended {
			end(exitHalted)
		}
		out.ended = ended
		o.batches = append(o.batches, out)
		if b.stop && !ended {
			end(exitHalted)
		}
	}
	o.exit, o.result = exit, result
	return o
}

// supRun runs c against the runtime.
func supRun(t *testing.T, c supCase) supOutcome {
	var o supOutcome
	driver := rt.Root("driver")
	defer driver.Interrupt()
	freshCalls := make(chan struct{}, 64)
	var calls int
	fresh := func(*rt.Task) ([]int, error) {
		calls++
		freshCalls <- struct{}{}
		if c.freshFails[calls] {
			return nil, errBoom
		}
		return []int{}, nil
	}
	var hold chan struct{}
	entered := make(chan struct{})
	step := func(_ *rt.Task, s []int, e int) (rdec, error) {
		switch e {
		case supDefect:
			panic("injected defect")
		case supDone:
			return rdec{decideDone, e}, nil
		case supHold:
			entered <- struct{}{}
			<-hold
			return rdec{decideIgnore, e}, nil
		case supFail:
			return rdec{}, errBoom
		}
		return rdec{decideStay, e}, nil
	}
	// Each entry has a work that runs until the entry ends: two works
	// alive at once is an incarnation whose work outlived its crash.
	var live atomic.Int32
	var overlap atomic.Bool
	work := func(wt *rt.Task, _ struct{}, _ func(int)) {
		if live.Add(1) > 1 {
			overlap.Store(true)
		}
		defer live.Add(-1)
		<-wt.Done()
	}
	a := MachineSupervise(Site("supervised"), driver, releaser{make(chan sleep)}, []int{}, fresh, c.limit, step,
		func(*rt.Task, []int, []int) (rdec, error) { return rdec{kind: decideIgnore}, nil },
		func([]int) time.Duration { return ActorNoTimer() }, noWork[[]int], func(struct{}) bool { return true }, work,
		func(d rdec) int { return d.kind },
		func(s []int, d rdec) []int {
			if d.kind == decideStay {
				return append(slices.Clone(s), d.e)
			}
			return s
		}, func(*rt.Task, rdec) {}, 64)
	text := func(err error) string {
		if err == nil {
			return ""
		}
		return err.Error()
	}
	drainResult := make(chan string, 1)
	modelEnded := false
	model := supModel(c)
	for bi, batch := range c.batches {
		var out supBatchOut
		if modelEnded {
			// The model says the actor has ended: admission fails.
			for _, op := range batch.ops {
				switch op.kind {
				case sendSignal:
					if err := a.Send(driver, op.e); !errors.Is(err, ActorStopped{}) {
						t.Fatalf("batch %d: Send after the end: %v, want Stopped", bi, err)
					}
				case askSignal:
					_, err := a.Ask(driver, op.e)
					out.answers = append(out.answers, supAnswer{err: text(err)})
				case stateRead:
					out.answers = append(out.answers, supAnswer{s: a.State(driver)})
				}
			}
			out.answers = append(out.answers, supAnswer{s: a.State(driver)})
			out.ended = true
			o.batches = append(o.batches, out)
			continue
		}
		hold = make(chan struct{})
		if err := a.Send(driver, supHold); err != nil {
			t.Fatalf("batch %d: the hold: %v", bi, err)
		}
		<-entered
		var chans []chan answer[[]int, rdec]
		for _, op := range append(slices.Clone(batch.ops), supOp{kind: stateRead}) {
			if op.kind == sendSignal {
				if err := a.Send(driver, op.e); err != nil {
					t.Fatalf("batch %d: Send while the runner is held: %v", bi, err)
				}
				continue
			}
			ch := make(chan answer[[]int, rdec], 1)
			if err := a.inbox.Push(driver, envelope[[]int, int, rdec]{kind: op.kind, sig: op.e, reply: &replySlot[[]int, rdec]{ch: ch}}); err != nil {
				t.Fatalf("batch %d: push while the runner is held: %v", bi, err)
			}
			chans = append(chans, ch)
		}
		if batch.drain {
			go func() {
				defer func() {
					if p := recover(); p != nil {
						drainResult <- "defect"
					}
				}()
				if err := a.Drain(rt.Root("drain")); err != nil {
					drainResult <- "failed"
					return
				}
				drainResult <- "ok"
			}()
			waitFor(t, "the Drain to close admission", a.inbox.isClosed)
		}
		close(hold)
		// The batch's last read is answered after every envelope before
		// it: then each of them must hold its one answer.
		last := <-chans[len(chans)-1]
		for i, ch := range chans[:len(chans)-1] {
			select {
			case x := <-ch:
				out.answers = append(out.answers, supAnswer{d: x.d, s: x.s, err: text(x.err)})
			default:
				t.Fatalf("seed case %+v: batch %d: envelope %d has no answer after the batch's last read", c, bi, i)
			}
		}
		out.answers = append(out.answers, supAnswer{d: last.d, s: last.s, err: text(last.err)})
		if batch.drain {
			<-drainResult
		}
		// The restarts the batch causes, then the end the model expects.
		want := model.batches[bi]
		for range want.fresh {
			select {
			case <-freshCalls:
				out.fresh++
			case <-a.done:
				// A fresh call comes before the end: take one queued.
				select {
				case <-freshCalls:
					out.fresh++
					continue
				default:
				}
				t.Fatalf("case %+v: batch %d: the actor ended after %d fresh states; the model restarts it %d times", c, bi, out.fresh, want.fresh)
			}
		}
		if want.ended {
			select {
			case <-a.done:
			case <-freshCalls:
				t.Fatalf("case %+v: batch %d: the actor restarted where the model ends it", c, bi)
			}
		}
		out.ended = want.ended
		modelEnded = want.ended
		if overlap.Load() {
			t.Fatalf("case %+v: batch %d: a work outlived its incarnation's crash", c, bi)
		}
		o.batches = append(o.batches, out)
		if batch.stop && !modelEnded {
			_, _ = catchStop(a)
			modelEnded = true
		}
	}
	err, raised := catchStop(a)
	switch {
	case raised != nil:
		o.result = "defect"
	case errors.Is(err, errBoom):
		o.result = "failed"
	case err != nil:
		o.result = "unexpected " + err.Error()
	default:
		o.result = "ok"
	}
	select {
	case <-freshCalls:
		t.Fatalf("case %+v: a fresh state after the last batch", c)
	default:
	}
	o.exit = ActorExitKind(driver, a)
	o.restarts = ActorRestarts(a)
	return o
}

func catchStop(a *Actor[[]int, int, rdec]) (err error, raised any) {
	defer func() { raised = recover() }()
	return a.Stop(rt.Root("stop")), nil
}

// TestActorSupervisionAgreesWithModel holds rule 16 against the reference
// for generated cases: the budget (Restarts and the exit), the fresh state
// of each incarnation (never the last one), one answer for every Ask and
// read (Stopped for the step's and the queued Asks at a crash), no restart
// after Done, a typed failure, a Stop or a Drain, and the defect as the
// exit once the budget is spent.
func TestActorSupervisionAgreesWithModel(t *testing.T) {
	before := runtime.NumGoroutine()
	reached := map[string]int{}
	for seed := int64(0); seed < 300; seed++ {
		c := supDraw(seed)
		want := supModel(c)
		got := supRun(t, c)
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("seed %d: case %+v\nrun   %+v\nmodel %+v", seed, c, got, want)
		}
		if want.restarts > 0 {
			reached["restart"]++
		}
		if want.exit == exitDefected {
			reached["exhausted"]++
		}
		if want.exit == exitFinished && want.restarts > 0 {
			reached["done after a restart"]++
		}
		for _, b := range want.batches {
			for _, x := range b.answers {
				if x.err == "actor.Stopped" && !b.ended {
					reached["ask stopped by a crash"]++
				}
			}
		}
		for i, b := range c.batches {
			if b.drain && want.batches[i].ended && want.exit == exitDefected {
				reached["defect in a drain"]++
			}
		}
		for r := range c.freshFails {
			if r <= want.restarts {
				reached["fresh failed"]++
			}
		}
	}
	for _, s := range []string{"restart", "exhausted", "done after a restart", "ask stopped by a crash", "defect in a drain", "fresh failed"} {
		if reached[s] == 0 {
			t.Errorf("no generated case reaches %q", s)
		}
	}
	waitFor(t, "goroutines to end", func() bool { return runtime.NumGoroutine() <= before })
}
