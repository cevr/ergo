package v1

import (
	"testing"

	"ergo.local/prototype/runtime/rt"
)

// TestRefUpdateIgnoresAStopInside: a stop requested while an update body
// runs is not a defect (REF-MASK). Each update's body asks its own task to
// stop, then polls as a lowered loop head does: the poll passes, the Ref
// holds the body's whole value and is not poisoned, and the caller's next
// poll after the update observes the stop. Mutant (ref-update-unmasked):
// end closes no mask that the update opened, because the update opens
// none, so the poll inside the body unwinds it and poisons the Ref.
func TestRefUpdateIgnoresAStopInside(t *testing.T) {
	clone := func(v []int) []int { return append([]int(nil), v...) }
	pair := func(task *rt.Task) func(v []int) struct {
		F0 int
		F1 []int
	} {
		return func(v []int) struct {
			F0 int
			F1 []int
		} {
			task.Interrupt()
			task.Check()
			return struct {
				F0 int
				F1 []int
			}{len(v), append(v, 9)}
		}
	}
	ops := map[string]func(task *rt.Task, r *Ref[[]int]){
		"Update": func(task *rt.Task, r *Ref[[]int]) {
			r.Update(task, func(v []int) []int { return pair(task)(v).F1 })
		},
		"Modify": func(task *rt.Task, r *Ref[[]int]) { RefModify(task, r, pair(task)) },
		"UpdateOwned": func(task *rt.Task, r *Ref[[]int]) {
			RefUpdateOwned(task, r, func(v []int) []int { return pair(task)(v).F1 }, clone, true, false)
		},
		"ModifyOwned": func(task *rt.Task, r *Ref[[]int]) { RefModifyOwned(task, r, pair(task), clone, true, false) },
	}
	for name, op := range ops {
		r := NewRef([]int{1})
		task := rt.Root("mask")
		var inside any
		func() {
			defer func() { inside = recover() }()
			op(task, r)
		}()
		if inside != nil {
			t.Errorf("%s: a stop inside the body unwound the update with %v", name, inside)
			continue
		}
		if got := r.Get(); len(got) != 2 || got[1] != 9 {
			t.Errorf("%s: the Ref holds %v, want the body's whole value [1 9]", name, got)
		}
		var after any
		func() {
			defer func() { after = recover() }()
			task.Check()
		}()
		if after != rt.Interrupted {
			t.Errorf("%s: the caller's next poll raised %v, want the stop", name, after)
		}
	}
}
