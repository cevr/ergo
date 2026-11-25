package eval

import (
	"testing"
)

// TestRefModifyAndUpdateAreAtomic verifies that sync.Modify and Ref.Update
// callbacks run atomically in the check-time evaluator: loop back-edges inside
// a callback never yield to another task mid-update (matching runtime/v1's
// mutex), while eval.Budget.Steps still bounds an infinite loop inside a
// callback.
//
// Mutant: eval-ref-modify-yields.
func TestRefModifyAndUpdateAreAtomic(t *testing.T) {
	pkg := checked(t, `import sync
import task

test ConcurrentModifyAtomic {
	let r = sync.NewRef(0)
	let t1 = task.Go() {
		for _ in 0..10 {
			let _ = sync.Modify(r, func(cur int) (int, int) {
				let mut next = cur
				for _ in 0..6 {
					next = next + 1
				}
				(next, next)
			})
		}
	}
	let t2 = task.Go() {
		for _ in 0..10 {
			let _ = sync.Modify(r, func(cur int) (int, int) {
				let mut next = cur
				for _ in 0..6 {
					next = next + 1
				}
				(next, next)
			})
		}
	}
	let t3 = task.Go() {
		for _ in 0..10 {
			let _ = sync.Modify(r, func(cur int) (int, int) {
				let mut next = cur
				for _ in 0..6 {
					next = next + 1
				}
				(next, next)
			})
		}
	}
	t1.Wait()
	t2.Wait()
	t3.Wait()
	assert r.Get() == 180
}

test ConcurrentUpdateAtomic {
	let r = sync.NewRef(0)
	let t1 = task.Go() {
		for _ in 0..10 {
			r.Update(func(cur int) int {
				let mut next = cur
				for _ in 0..6 {
					next = next + 1
				}
				next
			})
		}
	}
	let t2 = task.Go() {
		for _ in 0..10 {
			r.Update(func(cur int) int {
				let mut next = cur
				for _ in 0..6 {
					next = next + 1
				}
				next
			})
		}
	}
	let t3 = task.Go() {
		for _ in 0..10 {
			r.Update(func(cur int) int {
				let mut next = cur
				for _ in 0..6 {
					next = next + 1
				}
				next
			})
		}
	}
	t1.Wait()
	t2.Wait()
	t3.Wait()
	assert r.Get() == 180
}
`)
	for seed := uint64(1); seed <= 8; seed++ {
		b := DefaultBudget()
		b.Seed = seed
		rep, err := Run(pkg, b)
		if err != nil {
			t.Fatalf("seed %d: Run: %v", seed, err)
		}
		if len(rep.Results) != 2 {
			t.Fatalf("seed %d: got %d results, want 2", seed, len(rep.Results))
		}
		for _, res := range rep.Results {
			if res.Status != Passed {
				t.Fatalf("seed %d: %s = %s (%s)", seed, res.Name, res.Status, res.Reason)
			}
		}
	}

	// Step budget must still terminate an infinite loop inside Modify / Update.
	infPkg := checked(t, `import sync

test InfiniteModifyHitsBudget {
	let r = sync.NewRef(0)
	let _ = sync.Modify(r, func(cur int) (int, int) {
		let mut i = cur
		for i >= 0 {
			i = i + 1
		}
		(i, i)
	})
}

test InfiniteUpdateHitsBudget {
	let r = sync.NewRef(0)
	r.Update(func(cur int) int {
		let mut i = cur
		for i >= 0 {
			i = i + 1
		}
		i
	})
}
`)
	rep, err := Run(infPkg, Budget{Steps: 5_000, Depth: 8})
	if err != nil {
		t.Fatalf("infinite budget run: %v", err)
	}
	for _, res := range rep.Results {
		if res.Status != Incomplete {
			t.Fatalf("%s = %s (%s), want Incomplete", res.Name, res.Status, res.Reason)
		}
	}
}
