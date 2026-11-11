package v1

import (
	"fmt"
	"math/rand/v2"
	"os"
	"runtime"
	"strconv"
	"sync/atomic"
	"testing"

	"ergo.local/prototype/runtime/rt"
)

// TestAtomicLinearizableHistory compares generated histories with a
// sequential integer/boolean model, then records real increments in a
// tasks.Set. Tickets must form a sequential history that respects every
// nonoverlapping call, not only have the right total. The mutants are
// atomic-constructor, atomic-add-old-value, atomic-add-split,
// atomic-swap-old, atomic-cas-always and atomic-bool-cas-always.
// No explorer step can expose a Go atomic instruction's interleavings;
// this property uses the actual native calls and task lifetime boundary.
func TestAtomicLinearizableHistory(t *testing.T) {
	seed := uint64(93)
	if text := os.Getenv("ERGO_ATOMIC_SEED"); text != "" {
		var err error
		seed, err = strconv.ParseUint(text, 0, 64)
		if err != nil {
			t.Fatal(err)
		}
	}
	rng := rand.New(rand.NewPCG(seed, seed+1))
	for draw := range 32 {
		initial := int64(rng.IntN(201) - 100)
		flag := rng.IntN(2) == 1
		workers, each := 2+rng.IntN(5), 1+rng.IntN(8)
		parallelSource := fmt.Sprintf(`import atomic
import tasks
test History {
 let c = atomic.NewInt64(%d)
 let s = tasks.NewSet(tasks.FailFast)
 for _ in 0..%d {
  try tasks.Go(s, func() { for _ in 0..%d { _ = c.Add(1) } })
 }
 s.Wait()
 assert c.Load() == %d`, initial, workers, each, initial+int64(workers*each))
		source := fmt.Sprintf("import atomic\ntest Model {\n let c = atomic.NewInt64(%d)\n let flag = atomic.NewBool(%v)\n assert c.Load() == %d && flag.Load() == %v", initial, flag, initial, flag)
		fail := func(format string, args ...any) {
			t.Helper()
			t.Fatalf("seed %d draw %d; ERGO_ATOMIC_SEED=%d go test -run '^TestAtomicLinearizableHistory$' ./runtime/v1\nErgo source:\n%s\n}\n%s", seed, draw, seed, source, fmt.Sprintf(format, args...))
		}
		cell, toggle := NewInt64(initial), NewBool(flag)
		model, boolModel := initial, flag
		if cell.Load(nil) != model || toggle.Load(nil) != boolModel {
			fail("initial state: expected (%d,%v), actual (%d,%v)", model, boolModel, cell.Load(nil), toggle.Load(nil))
		}
		for step := range 40 {
			n := int64(rng.Uint64())
			b := rng.IntN(2) == 1
			switch step % 5 {
			case 0:
				source += fmt.Sprintf("\n c.Store(%d)\n flag.Store(%v)", n, b)
				cell.Store(nil, n)
				model = n
				toggle.Store(nil, b)
				boolModel = b
			case 1:
				model += n
				source += fmt.Sprintf("\n assert c.Add(%d) == %d", n, model)
				if got := cell.Add(nil, n); got != model {
					fail("Add: expected %d, actual %d", model, got)
				}
			case 2:
				source += fmt.Sprintf("\n assert c.Swap(%d) == %d\n assert flag.Swap(%v) == %v", n, model, b, boolModel)
				if got := cell.Swap(nil, n); got != model {
					fail("Swap: expected %d, actual %d", model, got)
				}
				model = n
				if got := toggle.Swap(nil, b); got != boolModel {
					fail("Bool.Swap: expected %v, actual %v", boolModel, got)
				}
				boolModel = b
			case 3:
				source += fmt.Sprintf("\n assert c.CompareAndSwap(%d, %d)\n assert flag.CompareAndSwap(%v, %v)", model, n, boolModel, b)
				if !cell.CompareAndSwap(nil, model, n) || !toggle.CompareAndSwap(nil, boolModel, b) {
					fail("matching CAS: expected true, actual false")
				}
				model, boolModel = n, b
			case 4:
				source += fmt.Sprintf("\n assert !c.CompareAndSwap(%d, %d)\n assert !flag.CompareAndSwap(%v, %v)", model+1, n, !boolModel, b)
				if cell.CompareAndSwap(nil, model+1, n) || toggle.CompareAndSwap(nil, !boolModel, b) {
					fail("mismatching CAS: expected false, actual true")
				}
			}
			source += fmt.Sprintf("\n assert c.Load() == %d && flag.Load() == %v", model, boolModel)
			if cell.Load(nil) != model || toggle.Load(nil) != boolModel {
				fail("step %d: expected (%d,%v), actual (%d,%v)", step, model, boolModel, cell.Load(nil), toggle.Load(nil))
			}
		}

		source = parallelSource
		type event struct {
			Invoke, Return uint64
			Ticket         int64
		}
		history := make([][]event, workers)
		winners := make([]bool, workers)
		claim := NewBool(false)
		cell.Store(nil, initial)
		var sequence atomic.Uint64
		root := rt.Root("atomic history")
		frame := NewScope(root)
		set := TasksNewSet(&frame, setFailFast, 0)
		ready, start := make(chan struct{}, workers), make(chan struct{})
		for worker := range workers {
			history[worker] = make([]event, each)
			if err := TasksGo(Site("atomic history"), root, set, func(_ *rt.Task) error {
				ready <- struct{}{}
				<-start
				winners[worker] = claim.CompareAndSwap(nil, false, true)
				for i := range each {
					invoke := sequence.Add(1)
					runtime.Gosched()
					ticket := cell.Add(nil, 1)
					history[worker][i] = event{invoke, sequence.Add(1), ticket}
				}
				return nil
			}); err != nil {
				frame.End(nil)
				fail("task admission: %v", err)
			}
		}
		for range workers {
			<-ready
		}
		close(start)
		err := set.Wait(root)
		frame.End(nil)
		if err != nil {
			fail("task set: %v", err)
		}
		winnerCount := 0
		for _, won := range winners {
			if won {
				winnerCount++
			}
		}
		if winnerCount != 1 || !claim.Load(nil) {
			fail("expected one flag winner and true flag; actual %v, %v", winners, claim.Load(nil))
		}
		var events []event
		for _, worker := range history {
			events = append(events, worker...)
		}
		seen := make([]bool, len(events))
		for _, e := range events {
			i := e.Ticket - initial - 1
			if i < 0 || i >= int64(len(seen)) || seen[i] {
				fail("expected unique tickets %d..%d, actual %v", initial+1, initial+int64(len(events)), events)
			}
			seen[i] = true
			for _, other := range events {
				if e.Return < other.Invoke && e.Ticket >= other.Ticket {
					fail("expected real-time ticket order, actual %v", events)
				}
			}
		}
		if want, got := initial+int64(len(events)), cell.Load(nil); got != want {
			fail("final: expected %d, actual %d; history %v", want, got, events)
		}
	}
}
