package v1

import (
	"maps"
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

// TestRefPoisonedByADefect: a panic in the function of an Update, a Modify
// or an owned Update reaches the caller and poisons the Ref, unlocked:
// each later Get, Set, Update and Modify raises the first panic value
// again (the mutant "no poison", and an end that keeps the lock).
func TestRefPoisonedByADefect(t *testing.T) {
	raised := func(op func()) (p any) {
		defer func() { p = recover() }()
		op()
		return nil
	}
	pair := func(v []int) struct {
		F0 int
		F1 []int
	} {
		return struct {
			F0 int
			F1 []int
		}{len(v), v}
	}
	clone := func(v []int) []int { return append([]int(nil), v...) }
	defects := map[string]func(r *Ref[[]int]){
		"Update": func(r *Ref[[]int]) { r.Update(func([]int) []int { panic("defect") }) },
		"Modify": func(r *Ref[[]int]) {
			RefModify(r, func([]int) struct {
				F0 int
				F1 []int
			} {
				panic("defect")
			})
		},
		"UpdateOwned": func(r *Ref[[]int]) {
			RefUpdateOwned(r, func(v []int) []int {
				v[0] = 9 // a write in place, then the defect
				panic("defect")
			}, clone, true)
		},
	}
	for name, defect := range defects {
		r := NewRef([]int{1})
		if p := raised(func() { defect(r) }); p != "defect" {
			t.Fatalf("%s: the caller got %v, want the defect", name, p)
		}
		later := map[string]func(){
			"Get":    func() { r.Get() },
			"Set":    func() { r.Set([]int{2}) },
			"Update": func() { r.Update(func(v []int) []int { return v }) },
			"Modify": func() { RefModify(r, pair) },
			"Owned":  func() { RefModifyOwned(r, pair, clone, true) },
		}
		for op, f := range later {
			if p := raised(f); p != "defect" {
				t.Errorf("%s, then %s: raised %v, want the first defect again", name, op, p)
			}
		}
	}
}

// TestRefOwnsItsValue: an owned Update writes the held value in place, yet
// no value reached from outside the cell changes: the value given to
// NewRef, a value Get returned, a value Set stored, and a value a plain
// Update stored, each shared, is copied before the owned write (the
// mutants "Get without a share", "NewRef, Set or Update unshared"). After
// an owned Update whose result is fresh, the next one writes the same Map
// with no copy (the mutant "copy always", which the benchmark sees too).
func TestRefOwnsItsValue(t *testing.T) {
	clones := 0
	clone := func(m map[int]int) map[int]int {
		clones++
		return maps.Clone(m)
	}
	given := map[int]int{0: 0}
	r := NewRef(given)
	own := func(k int) {
		RefUpdateOwned(r, func(m map[int]int) map[int]int {
			m[k] = k
			return m
		}, clone, true)
	}
	own(1)
	if len(given) != 1 {
		t.Errorf("an owned Update wrote the value given to NewRef: %v", given)
	}
	own(2)
	if clones != 1 {
		t.Errorf("two owned Updates after NewRef made %d copies, want 1", clones)
	}
	got := r.Get()
	own(3)
	if len(got) != 3 {
		t.Errorf("an owned Update wrote a value Get returned: %v", got)
	}
	set := map[int]int{7: 7}
	r.Set(set)
	own(4)
	if len(set) != 1 {
		t.Errorf("an owned Update wrote a value Set stored: %v", set)
	}
	outside := map[int]int{8: 8}
	r.Update(func(map[int]int) map[int]int { return outside })
	own(5)
	if len(outside) != 1 {
		t.Errorf("an owned Update wrote a value a plain Update stored: %v", outside)
	}
	if want := map[int]int{8: 8, 5: 5}; !maps.Equal(r.Get(), want) {
		t.Errorf("the Ref holds %v, want %v", r.Get(), want)
	}
}

// TestRefOwnedSharedByTasks: owned Updates and Gets from several
// goroutines at once lose no Update and never write a value a Get
// returned. A copy made outside the lock (the mutant) loses Updates.
func TestRefOwnedSharedByTasks(t *testing.T) {
	const workers, each = 6, 300
	r := NewRef(map[int]int{})
	clone := func(m map[int]int) map[int]int { return maps.Clone(m) }
	var wg sync.WaitGroup
	for w := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range each {
				RefUpdateOwned(r, func(m map[int]int) map[int]int {
					runtime.Gosched()
					m[w*each+i] = i
					return m
				}, clone, true)
				if i%3 == 0 {
					g := r.Get()
					n := len(g)
					runtime.Gosched()
					if len(g) != n {
						t.Errorf("a value Get returned changed from %d to %d entries", n, len(g))
						return
					}
				}
			}
		}()
	}
	wg.Wait()
	if got := len(r.Get()); got != workers*each {
		t.Errorf("after %d owned Updates the Map has %d entries", workers*each, got)
	}
}
