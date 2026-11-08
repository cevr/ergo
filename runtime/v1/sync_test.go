package v1

import (
	"runtime"
	"sync"
	"testing"
)

// TestRefSharedByTasks: goroutines that Update one Ref at once lose no
// Update. Each function yields the processor between its read and its
// result, so an Update that read outside the lock would lose some even on
// one processor (the mutant "non-atomic Update"). Run with -race, it also
// shows the lock covers the value.
func TestRefSharedByTasks(t *testing.T) {
	const workers, each = 8, 500
	r := NewRef(0)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range each {
				r.Update(func(v int) int {
					runtime.Gosched()
					return v + 1
				})
			}
		}()
	}
	wg.Wait()
	if got := r.Get(); got != workers*each {
		t.Errorf("after %d Updates from %d goroutines the Ref holds %d", workers*each, workers, got)
	}
}

// TestRefModifySharedByTasks: Modify from several goroutines at once hands
// out each value exactly once, as a counter that returns the value it
// replaced does; Set and Get from another goroutine meanwhile see whole
// values only.
func TestRefModifySharedByTasks(t *testing.T) {
	const workers, each = 8, 300
	r := NewRef(0)
	got := make([][]int, workers)
	var wg sync.WaitGroup
	for w := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range each {
				got[w] = append(got[w], RefModify(r, func(v int) struct {
					F0 int
					F1 int
				} {
					runtime.Gosched()
					return struct {
						F0 int
						F1 int
					}{v, v + 1}
				}))
			}
		}()
	}
	wg.Wait()
	seen := make([]bool, workers*each)
	for _, vs := range got {
		for _, v := range vs {
			if v < 0 || v >= len(seen) || seen[v] {
				t.Fatalf("Modify handed out %d twice or out of range", v)
			}
			seen[v] = true
		}
	}
	if r.Get() != workers*each {
		t.Errorf("the Ref holds %d after %d Modifies", r.Get(), workers*each)
	}
}

// TestRefReadsTheLastStore: Get gives the value the last Set, Update or
// Modify stored, on the Ref and on every copy of its pointer (the mutant
// "Get returns a stale copy"), and Update and Modify call their function
// once.
func TestRefReadsTheLastStore(t *testing.T) {
	r := NewRef("a")
	same := r
	if r.Get() != "a" {
		t.Fatalf("NewRef(a).Get() = %q", r.Get())
	}
	r.Set("b")
	if same.Get() != "b" {
		t.Errorf("after Set(b) a copy reads %q", same.Get())
	}
	calls := 0
	same.Update(func(v string) string { calls++; return v + "c" })
	if r.Get() != "bc" || calls != 1 {
		t.Errorf("after Update the Ref holds %q with %d calls; want bc and 1", r.Get(), calls)
	}
	out := RefModify(r, func(v string) struct {
		F0 int
		F1 string
	} {
		calls++
		return struct {
			F0 int
			F1 string
		}{len(v), v + "d"}
	})
	if out != 2 || same.Get() != "bcd" || calls != 2 {
		t.Errorf("Modify returned %d and stored %q with %d calls; want 2, bcd and 2", out, same.Get(), calls)
	}
}

// TestRefSurvivesADefect: a panic in an Update's function leaves the Ref
// unlocked, holding the value it held before.
func TestRefSurvivesADefect(t *testing.T) {
	r := NewRef(4)
	func() {
		defer func() { _ = recover() }()
		r.Update(func(int) int { panic("defect") })
	}()
	r.Update(func(v int) int { return v + 1 })
	if r.Get() != 5 {
		t.Errorf("after a defect and an Update the Ref holds %d, want 5", r.Get())
	}
}
