package v1

import (
	"testing"

	"ergo.local/prototype/runtime/rt"
)

func taskAllocBody(*rt.Task) (int, error) { return 1, nil }

var taskAllocRoot = rt.Root("allocs")

func taskAllocRelease(o *Task[int]) { o.Close(taskAllocRoot) }

// TestTaskOwnsWithoutAllocating: a task.Go, its owner's registration and
// its Wait, written as the lowering writes them, allocate what rt's own
// spawn and wait allocate (the task and its goroutine's start) and nothing
// more: the Task holds its handle (PARK1) and its first owner's entry,
// and its release needs no closure (PARK2). Its bodies and release capture
// nothing, so the count is the runtime's alone. Mutant
// owning-skips-self-entry (Owning makes an entry and a closure for a
// Task) is killed only here: every other test checks what a release does,
// not what it costs.
func TestTaskOwnsWithoutAllocating(t *testing.T) {
	fs := NewScope(taskAllocRoot)
	defer fs.End(nil)
	v1 := testing.AllocsPerRun(1000, func() {
		task := Go[int](&fs, Site("allocs"), taskAllocBody)
		own := fs.Owner()
		Owning(own, task, taskAllocRelease)
		if _, err := Hand(own, task).Wait(); err != nil {
			t.Fatal(err)
		}
	})
	base := testing.AllocsPerRun(1000, func() {
		s := rt.NewScope(taskAllocRoot)
		h := rt.Go(&s, "allocs", taskAllocBody)
		if _, err := h.Wait(); err != nil {
			t.Fatal(err)
		}
		s.End(nil)
	})
	if v1 != base {
		t.Fatalf("task.Go, Owning and Wait allocate %v times; rt's spawn and wait %v; want equal", v1, base)
	}
}
