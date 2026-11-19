package v1

import (
	"testing"
	"unsafe"

	"ergo.local/prototype/runtime/rt"
)

func taskAllocBody(*rt.Task) (int, error) { return 1, nil }

// taskAllocCaps are a lowered block's captures: the frame's environment
// (a pointer) and a loop local.
type taskAllocCaps struct {
	env *int
	i   int
}

func taskAllocWithBody(_ *rt.Task, c *taskAllocCaps) (int, error) { return *c.env + c.i, nil }

// TestTaskWithCapturesAllocatesOnce: GoWith, the lowering's task.Go of a
// block whose captures it gathers, its owner's registration with
// ReleaseTask and its Wait allocate what rt's own spawn and wait allocate:
// the captures live in the Task's allocation, and the block and its
// release capture nothing (GP-SPAWN2). The block reads its captures, and
// the Task keeps none of them once the block returned.
//
// Mutants: gp-spawn2-gowith-closure (GoWith is Go of a closure over the
// captures: one more allocation), gp-spawn2-captures-kept (the Task keeps
// the captures past the block).
func TestTaskWithCapturesAllocatesOnce(t *testing.T) {
	fs := NewScope(taskAllocRoot)
	defer fs.End(nil)
	env := 40
	var last *Task[int]
	got := testing.AllocsPerRun(1000, func() {
		task := GoWith[int, taskAllocCaps](&fs, Site("allocs"), taskAllocCaps{env: &env, i: 2}, taskAllocWithBody)
		own := fs.Owner()
		Owning(own, task, ReleaseTask[int])
		v, err := Hand(own, task).Wait()
		if err != nil || v != 42 {
			t.Fatalf("Wait = %v, %v; want 42, nil", v, err)
		}
		last = task
	})
	base := testing.AllocsPerRun(1000, func() {
		s := rt.NewScope(taskAllocRoot)
		h := rt.Go(&s, "allocs", taskAllocBody)
		if _, err := h.Wait(); err != nil {
			t.Fatal(err)
		}
		s.End(nil)
	})
	if got != base {
		t.Fatalf("GoWith, Owning and Wait allocate %v times; rt's spawn and wait %v; want equal", got, base)
	}
	if x := (*taskWith[int, taskAllocCaps])(unsafe.Pointer(last)); x.c != (taskAllocCaps{}) {
		t.Fatalf("the Task keeps its block's captures after the block returned: %+v", x.c)
	}
}

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
