package rt

import (
	"go/ast"
	"go/parser"
	"go/token"
	"math/rand/v2"
	"testing"
)

// TestStepBudgetFastPathUntouched checks that Check and Stopped keep their
// single-load fast path with no budget counter touched when unbudgeted, and
// that an unbudgeted task never sets flagPolled or flagBudget. Kills
// budget-counts-fast-path.
func TestStepBudgetFastPathUntouched(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "task.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	checked := map[string]bool{}
	for _, d := range file.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Recv == nil {
			continue
		}
		if fn.Name.Name != "Check" && fn.Name.Name != "Stopped" {
			continue
		}
		checked[fn.Name.Name] = true
		if fn.Name.Name == "Check" {
			if len(fn.Body.List) != 1 {
				t.Fatalf("Task.Check has %d statements; want 1 guarded fast-path if", len(fn.Body.List))
			}
			if _, ok := fn.Body.List[0].(*ast.IfStmt); !ok {
				t.Fatalf("Task.Check body is %T; want *ast.IfStmt guarding checkSlow", fn.Body.List[0])
			}
		} else {
			if len(fn.Body.List) != 2 {
				t.Fatalf("Task.Stopped has %d statements; want 2 (fast-path if + return stoppedSlow)", len(fn.Body.List))
			}
			if _, ok := fn.Body.List[0].(*ast.IfStmt); !ok {
				t.Fatalf("Task.Stopped first statement is %T; want *ast.IfStmt", fn.Body.List[0])
			}
		}
	}
	if !checked["Check"] || !checked["Stopped"] {
		t.Fatalf("did not find Check and Stopped in task.go: %v", checked)
	}

	root := Root("unbudgeted")
	if root.stopped.v.Load()&(flagPolled|flagBudget) != 0 {
		t.Fatalf("Root has poll/budget flags set: %#x", root.stopped.v.Load())
	}
	s := NewScope(root)
	h := GoWithBudget(&s, "zero-budget", 0, func(ct *Task) (int, error) {
		if ct.stopped.v.Load()&(flagPolled|flagBudget) != 0 {
			t.Errorf("zero-budget child has poll/budget flags set: %#x", ct.stopped.v.Load())
		}
		for range 100 {
			ct.Check()
		}
		return 1, nil
	})
	v, err := h.Wait()
	s.End(nil)
	if err != nil || v != 1 || h.Task().BudgetExceeded() {
		t.Fatalf("zero-budget child: v=%d err=%v exceeded=%v", v, err, h.Task().BudgetExceeded())
	}
	if !raceEnabled {
		if allocs := testing.AllocsPerRun(1000, func() { root.Check() }); allocs != 0 {
			t.Fatalf("unbudgeted Check allocated %v/op; want 0", allocs)
		}
	}
}

// TestStepBudgetProperty generates loops of impure frames with drawn step
// budgets and verifies exact termination at the budget's point count: a loop
// with K <= N points completes, and a loop with K > N points completes
// exactly N iterations before stopping with BudgetExceeded() == true. Kills
// budget-off-by-one.
func TestStepBudgetProperty(t *testing.T) {
	rng := rand.New(rand.NewPCG(42, 99))
	root := Root("step-budget-prop")

	for trial := range 80 {
		limit := int64(1 + rng.IntN(40))
		// Exercise exact boundary (K == limit, K == limit+1) plus drawn K.
		ks := []int64{limit, limit + 1, int64(rng.IntN(60))}
		for _, k := range ks {
			s := NewScope(root)
			var ran int64
			h := GoWithBudget(&s, "loop", limit, func(ct *Task) (int64, error) {
				for range k {
					ct.Check()
					ran++
				}
				return ran, nil
			})
			got, err := h.Wait()
			s.End(nil)
			if k <= limit {
				if err != nil || got != k || h.Task().BudgetExceeded() {
					t.Fatalf("trial %d (limit=%d, k=%d): got (%d, %v, exceeded=%v); want (%d, nil, false)",
						trial, limit, k, got, err, h.Task().BudgetExceeded(), k)
				}
			} else {
				if err != Interrupted || ran != limit || !h.Task().BudgetExceeded() {
					t.Fatalf("trial %d (limit=%d, k=%d): got (ran=%d, err=%v, exceeded=%v); want (ran=%d, Interrupted, true)",
						trial, limit, k, ran, err, h.Task().BudgetExceeded(), limit)
				}
			}
		}
	}

	// Also verify back-edge poll loop shape (work then Check at back edge):
	// K <= limit completes all K iterations; K > limit runs K=limit+1 body
	// passes and stops at the (limit+1)-th back-edge poll.
	for limit := int64(1); limit <= 12; limit++ {
		for _, k := range []int64{limit - 1, limit, limit + 1, limit + 5} {
			s := NewScope(root)
			var iters int64
			h := GoWithBudget(&s, "back-edge", limit, func(ct *Task) (int64, error) {
				for range k {
					iters++
					ct.Check()
				}
				return iters, nil
			})
			got, err := h.Wait()
			s.End(nil)
			if k <= limit {
				if err != nil || got != k || h.Task().BudgetExceeded() {
					t.Fatalf("back-edge (limit=%d, k=%d): got (%d, %v, exceeded=%v); want (%d, nil, false)",
						limit, k, got, err, h.Task().BudgetExceeded(), k)
				}
			} else {
				if err != Interrupted || iters != limit+1 || !h.Task().BudgetExceeded() {
					t.Fatalf("back-edge (limit=%d, k=%d): got (iters=%d, err=%v, exceeded=%v); want (iters=%d, Interrupted, true)",
						limit, k, iters, err, h.Task().BudgetExceeded(), limit+1)
				}
			}
		}
	}
}

// TestStepBudgetRespectsMask verifies that a budgeted task never interrupts
// while inside a masked region (such as cleanup or release), and interrupts
// at the next unmasked interruption point once unmasked. Kills
// budget-ignores-mask.
func TestStepBudgetRespectsMask(t *testing.T) {
	root := Root("step-budget-mask")
	s := NewScope(root)

	// Case 1: enter mask before exhausting budget, run past limit inside mask,
	// unmask, then hit next unmasked Check.
	var maskedDone, unmaskedAfter int
	h1 := GoWithBudget(&s, "masked-work", 3, func(ct *Task) (int, error) {
		for range 3 {
			ct.Check()
		}
		m := ct.Mask()
		for range 10 {
			ct.Check()
			maskedDone++
		}
		m.Unmask()
		ct.Check()
		unmaskedAfter++
		return 1, nil
	})
	_, err1 := h1.Wait()
	if err1 != Interrupted || !h1.Task().BudgetExceeded() {
		t.Fatalf("case 1: got err=%v exceeded=%v; want Interrupted, true", err1, h1.Task().BudgetExceeded())
	}
	if maskedDone != 10 || unmaskedAfter != 0 {
		t.Fatalf("case 1: maskedDone=%d unmaskedAfter=%d; want 10, 0", maskedDone, unmaskedAfter)
	}

	// Case 2: exhaust budget, then run masked cleanup with many Check calls:
	// cleanup must complete every step without interrupting.
	var cleanupSteps int
	h2 := GoWithBudget(&s, "cleanup-masked", 4, func(ct *Task) (int, error) {
		defer func() {
			m := ct.Mask()
			defer m.Unmask()
			for range 20 {
				ct.Check()
				cleanupSteps++
			}
		}()
		for {
			ct.Check()
		}
	})
	_, err2 := h2.Wait()
	s.End(nil)
	if err2 != Interrupted || !h2.Task().BudgetExceeded() {
		t.Fatalf("case 2: got err=%v exceeded=%v; want Interrupted, true", err2, h2.Task().BudgetExceeded())
	}
	if cleanupSteps != 20 {
		t.Fatalf("case 2: cleanupSteps=%d; want 20", cleanupSteps)
	}
}

// TestStepBudgetSiblingIsolation verifies that a step budget on one child
// task is never inherited by its parent or sibling tasks, while an
// unshielded child of the budgeted task does share the budget. Kills
// budget-inherited-by-sibling.
func TestStepBudgetSiblingIsolation(t *testing.T) {
	root := Root("step-budget-sibling")
	s := NewScope(root)

	// Start a budgeted child with a tiny limit.
	budgeted := GoWithBudget(&s, "budgeted", 5, func(ct *Task) (int, error) {
		for {
			ct.Check()
		}
	})
	// Start an unbudgeted sibling from the same parent: it must run well
	// past 5 steps without stopping or seeing BudgetExceeded.
	var siblingSteps int
	sibling := Go(&s, "sibling", func(ct *Task) (int, error) {
		for range 500 {
			ct.Check()
			siblingSteps++
		}
		return siblingSteps, nil
	})

	_, bErr := budgeted.Wait()
	sVal, sErr := sibling.Wait()
	if bErr != Interrupted || !budgeted.Task().BudgetExceeded() {
		t.Fatalf("budgeted child: err=%v exceeded=%v; want Interrupted, true", bErr, budgeted.Task().BudgetExceeded())
	}
	if sErr != nil || sVal != 500 || sibling.Task().BudgetExceeded() {
		t.Fatalf("sibling leaked budget: val=%d err=%v exceeded=%v", sVal, sErr, sibling.Task().BudgetExceeded())
	}
	if root.stopped.v.Load()&(flagBudget|flagBudgetSpent|flagStopped) != 0 {
		t.Fatalf("parent leaked budget flags: %#x", root.stopped.v.Load())
	}

	// Start a second budgeted parent whose unshielded child spends the budget.
	parent := GoWithBudget(&s, "budgeted-parent", 6, func(ct *Task) (int, error) {
		cs := NewScope(ct)
		child := Go(&cs, "inner-child", func(gt *Task) (int, error) {
			for {
				gt.Check()
			}
		})
		v, err := child.Wait()
		cs.End(&err)
		return v, err
	})
	_, pErr := parent.Wait()
	s.End(nil)
	if pErr != Interrupted || !parent.Task().BudgetExceeded() {
		t.Fatalf("budgeted parent with inner child: err=%v exceeded=%v; want Interrupted, true", pErr, parent.Task().BudgetExceeded())
	}
}

// TestStepBudgetExternalStopAndPollHook verifies that an external Interrupt
// before budget exhaustion leaves BudgetExceeded false, and that WhenPolled
// hooks coexist cleanly with an active step budget.
func TestStepBudgetExternalStopAndPollHook(t *testing.T) {
	root := Root("step-budget-ext")
	s := NewScope(root)

	// External stop before budget expires: BudgetExceeded must stay false.
	ready := make(chan struct{})
	h := GoWithBudget(&s, "ext-stop", 1000, func(ct *Task) (int, error) {
		ct.Check()
		close(ready)
		<-ct.Done()
		ct.Check()
		return 1, nil
	})
	<-ready
	h.Task().Interrupt()
	_, err := h.Wait()
	if err != Interrupted || h.Task().BudgetExceeded() {
		t.Fatalf("external stop: err=%v exceeded=%v; want Interrupted, false", err, h.Task().BudgetExceeded())
	}

	// WhenPolled hook armed on a budgeted task must fire once and leave the
	// step budget intact.
	var hookFired int
	var steps int
	h2 := GoWithBudget(&s, "polled-budget", 4, func(ct *Task) (int, error) {
		var ph PollHook
		p := WhenPolled(ct, &ph, func() { hookFired++ })
		defer p.End()
		for {
			ct.Check()
			steps++
		}
	})
	_, err2 := h2.Wait()
	s.End(nil)
	if err2 != Interrupted || !h2.Task().BudgetExceeded() || hookFired != 1 || steps != 4 {
		t.Fatalf("polled-budget: err=%v exceeded=%v hookFired=%d steps=%d; want Interrupted, true, 1, 4",
			err2, h2.Task().BudgetExceeded(), hookFired, steps)
	}
}
