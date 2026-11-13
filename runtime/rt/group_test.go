package rt

import (
	"sync"
	"testing"
	"time"
)

// TestGroupMembersLeave: a group adds no live member to its parent's auto
// clock domain, and each member that RunIn ran leaves the domain and the
// task tree when it returns, so a server that runs many requests keeps
// neither (HTTPServe runs one member per request). It counts the domain's
// live members and the group's linked children, at a few members run from
// other goroutines.
//
// Mutants it kills (testdata/mutants.json): gx4-member-stays-in-domain,
// gx4-member-stays-linked, gx4-group-counted-live.
func TestGroupMembersLeave(t *testing.T) {
	parent := Root("server")
	v := NewAuto(parent, time.Unix(0, 0))
	live := func() int {
		v.mu.Lock()
		defer v.mu.Unlock()
		return v.live
	}
	before := live()
	g := NewGroup(parent, "requests")
	if got := live(); got != before {
		t.Fatalf("the group counts as a live member: %d, want %d", got, before)
	}
	var wg sync.WaitGroup
	for i := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n, err := RunIn(g, "request", func(m *Task) (int, error) {
				if m.member.Load() == nil {
					t.Error("a member is not in the group's clock domain")
				}
				return i, nil
			})
			if err != nil || n != i {
				t.Errorf("member %d: %d, %v", i, n, err)
			}
		}()
	}
	wg.Wait()
	if got := live(); got != before {
		t.Fatalf("members stay live in the domain: %d, want %d", got, before)
	}
	g.task.mu.Lock()
	first := g.task.first
	g.task.mu.Unlock()
	if first != nil {
		t.Fatalf("a returned member stays linked under the group")
	}
	g.End()
	if parent.first != nil {
		t.Fatalf("an ended group stays linked under its parent")
	}
}
