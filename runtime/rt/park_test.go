package rt

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

// lawParker is a park word body as the laws see it.
type lawParker interface {
	park()
	unpark()
	parkedNow() bool
	pending() bool
}

// parkBodies are the bodies the laws run on: the stock body always, and
// the word body in a build that has it (park_word_test.go).
var parkBodies = map[string]func() lawParker{
	"stock": func() lawParker { return new(stockParker) },
}

// seedsFrom reads env, a replay of one seed, or gives seeds 1..n.
func seedsFrom(t *testing.T, env string, n uint64) (first, count uint64) {
	if s := os.Getenv(env); s != "" {
		v, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatalf("%s %q: %v", env, s, err)
		}
		return v, 1
	}
	return 1, n
}

// TestParkWordLaws runs seeded programs of wakes ("U", from the test's
// goroutine) and parks ("P", by one waiter goroutine) on each park word
// body against the model: one bool, a pending wake. The laws:
//
//   - no lost wake: a park with a wake pending returns and never sleeps;
//   - one wake per park: wakes do not add up, so a park with none pending
//     sleeps, and after each step a wake is pending exactly when the model
//     has one;
//   - a wake reaches a sleeping waiter: a U while the waiter sleeps makes
//     its park return.
//
// A failing seed prints the program and the replay command
// (ERGO_PARK_SEED=n).
//
// Mutants (testdata/mutants.json): park-misses-early-wake,
// unpark-drops-pending, unpark-skips-sleeper,
// stock-park-misses-early-wake, stock-unpark-drops-pending.
func TestParkWordLaws(t *testing.T) {
	first, n := seedsFrom(t, "ERGO_PARK_SEED", 300)
	for name, mk := range parkBodies {
		t.Run(name, func(t *testing.T) {
			for seed := first; seed < first+n; seed++ {
				prog := genParkProgram(seed)
				if why := runParkProgram(mk(), prog); why != "" {
					t.Fatalf("seed %d, body %s: %s\nprogram: %s\nreplay: ERGO_PARK_SEED=%d go test -run '^TestParkWordLaws$' ./runtime/rt", seed, name, why, prog, seed)
				}
			}
		})
	}
}

// genParkProgram is a run of U and P steps, with a U after every P that
// would sleep at the end, so the waiter always ends.
func genParkProgram(seed uint64) string {
	r := rand.New(rand.NewPCG(seed, 0x9a2c))
	var b strings.Builder
	for range 1 + r.IntN(12) {
		if r.IntN(2) == 0 {
			b.WriteByte('U')
		} else {
			b.WriteByte('P')
		}
	}
	return b.String()
}

// runParkProgram runs prog on p and returns why it broke a law, or "".
func runParkProgram(p lawParker, prog string) string {
	parks := make(chan struct{})
	returned := make(chan struct{})
	go func() {
		for range parks {
			p.park()
			returned <- struct{}{}
		}
	}()
	defer close(parks)
	pending, sleeping := false, false
	for i, op := range prog {
		switch op {
		case 'U':
			p.unpark()
			if sleeping {
				<-returned // a missing wake hangs here: the test's deadline names it
				sleeping = false
			} else {
				pending = true
			}
		case 'P':
			if sleeping {
				continue // one waiter: a P while it sleeps is no step
			}
			parks <- struct{}{}
			if pending {
				// The park must return without sleeping.
				for done := false; !done; {
					select {
					case <-returned:
						done = true
					default:
						if p.parkedNow() {
							p.unpark()
							<-returned
							return fmt.Sprintf("step %d: the park slept with a wake pending (a lost wake)", i)
						}
						runtime.Gosched()
					}
				}
				pending = false
			} else {
				// The park must sleep.
				for !p.parkedNow() {
					select {
					case <-returned:
						return fmt.Sprintf("step %d: the park returned with no wake pending", i)
					default:
						runtime.Gosched()
					}
				}
				sleeping = true
			}
		}
		if !sleeping && p.pending() != pending {
			return fmt.Sprintf("step %d: a wake pending is %v, want %v", i, p.pending(), pending)
		}
	}
	if sleeping {
		p.unpark()
		<-returned
	}
	return ""
}

// TestParkWordRaceKeepsWake races each wake with its park: the waiter
// parks while the test's goroutine wakes it after a seeded spin, so some
// wakes land between the park's first look and its commit. The law: once
// the wake returned, the waiter never sleeps, it returns. TestParkWordLaws
// orders every step, so it never reaches that window.
//
// Mutant (testdata/mutants.json): park-commit-drops-wake. The stock
// body's window between its two looks is a few instructions wide, which
// this race does not reach: an explorer model of the word is a follow-up.
func TestParkWordRaceKeepsWake(t *testing.T) {
	if runtime.GOMAXPROCS(0) < 2 {
		t.Skip("the race needs two threads")
	}
	for name, mk := range parkBodies {
		t.Run(name, func(t *testing.T) {
			p := mk()
			// The waiter spins on go for its next round, so it runs on its
			// own thread and parks at once, while the test spins a seeded
			// count and wakes it.
			const rounds = 20000
			var started, returned atomic.Int64
			go func() {
				for i := int64(1); i <= rounds; i++ {
					for started.Load() < i {
						runtime.Gosched()
					}
					p.park()
					returned.Store(i)
				}
			}()
			r := rand.New(rand.NewPCG(1, 0x7ace))
			sink := 0
			for i := int64(1); i <= rounds; i++ {
				started.Store(i)
				for j := r.IntN(2000); j > 0; j-- {
					sink += j
				}
				p.unpark()
				for returned.Load() < i {
					if p.parkedNow() {
						p.unpark()
						t.Fatalf("round %d: the waiter sleeps after its wake returned: a wake that came while it parked was lost", i)
					}
					runtime.Gosched()
				}
			}
			_ = sink
		})
	}
}

// TestJoinLaws runs seeded joins against their model on the body this
// build uses (the gate runs it with -tags ergo_stockpark too): the child
// returns a value, a typed failure or a defect, before or after its owner
// parks in Wait; or the owner is stopped while it sleeps in Wait. The
// laws: Wait returns the task's value, or its failure as itself, or a
// defect; a stop that comes while the owner sleeps on its word ends the
// Wait with the interruption, and the task is stopped and drained. The
// owner sleeps on its park word before the event when the case says
// "after", which the test waits for.
//
// Mutants (testdata/mutants.json): finish-skips-resume,
// interrupt-skips-resume, wait-ignores-stop, wait-ignores-ended.
func TestJoinLaws(t *testing.T) {
	first, n := seedsFrom(t, "ERGO_JOIN_SEED", 120)
	for seed := first; seed < first+n; seed++ {
		c := genJoinCase(seed)
		if why := runJoinCase(c); why != "" {
			t.Fatalf("seed %d: %s\ncase: %+v\nreplay: ERGO_JOIN_SEED=%d go test -run '^TestJoinLaws$' ./runtime/rt", seed, why, c, seed)
		}
	}
}

type joinCase struct {
	Exit  string // "value", "failure", "defect" or "stop"
	After bool   // the event comes after the owner sleeps in Wait
	Value int
}

func genJoinCase(seed uint64) joinCase {
	r := rand.New(rand.NewPCG(seed, 0x701e))
	exits := []string{"value", "failure", "defect", "stop"}
	c := joinCase{Exit: exits[r.IntN(len(exits))], After: r.IntN(2) == 0, Value: r.IntN(1000)}
	if c.Exit == "stop" {
		c.After = true // a stop before the Wait is TestWaitIsAnInterruptionPoint's
	}
	return c
}

func runJoinCase(c joinCase) string {
	root := Root("main")
	s := NewScope(root)
	defer endScope(&s)
	release := make(chan struct{})
	drained := false
	var waited atomic.Pointer[Task]
	outer := Go(&s, "owner", func(owner *Task) (string, error) {
		waited.Store(owner)
		return Scoped(owner, nil, func(s *Scope) (string, error) {
			// A shielded child: the owner's stop does not cascade into it,
			// so only Wait's own stop and drain end it.
			start := Go[int]
			if c.Exit == "stop" {
				start = GoShielded[int]
			}
			h := start(s, "child", func(task *Task) (int, error) {
				if c.Exit == "stop" {
					<-task.Done()
					drained = true
					return 0, Interrupted
				}
				if c.After {
					<-release
				}
				switch c.Exit {
				case "failure":
					return 0, errBody
				case "defect":
					panic("child defect")
				}
				return c.Value, nil
			})
			if !c.After {
				waitUntil(h.finished)
			}
			v, err := h.Wait()
			return fmt.Sprintf("%d %v", v, err), nil
		})
	})
	if c.After {
		waitUntil(func() bool { w := waited.Load(); return w != nil && w.wake.parkedNow() })
		if c.Exit == "stop" {
			waited.Load().Interrupt()
		} else {
			close(release)
		}
	}
	got, err := outer.Wait()
	switch c.Exit {
	case "value":
		if want := fmt.Sprintf("%d <nil>", c.Value); got != want || err != nil {
			return fmt.Sprintf("Wait gave %q, %v; want %q", got, err, want)
		}
	case "failure":
		if want := fmt.Sprintf("0 %v", errBody); got != want || err != nil {
			return fmt.Sprintf("Wait gave %q, %v; want the failure as itself", got, err)
		}
	case "defect":
		if !strings.HasPrefix(got, "0 ") || !strings.Contains(got, "child defect") {
			return fmt.Sprintf("Wait gave %q; want the defect", got)
		}
	case "stop":
		if !errors.Is(err, Interrupted) || !drained {
			return fmt.Sprintf("the stopped owner's exit is %v (child drained %v); want the interruption after the drain", err, drained)
		}
	}
	return ""
}
