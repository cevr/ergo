package v1

import (
	"sync"
	"sync/atomic"

	"ergo.local/prototype/runtime/rt"
)

// Named points (std testing's At and Point; RT-HOOK). A library marks a
// place in its code where another task's event decides what it reads (a
// commit between two reads of an actor, an answer posted after a check)
// with Point(name); a test arms the point with At(name, f), and the next
// task that reaches it runs f there, on its own task, before it goes on.
// So the test lands the event at that exact place, every run, where it
// would otherwise race for it. Prior art: Go's testing/synctest makes
// "every other task waits" a point a test can wait for; Loom and the
// explored scheduler (explore_on.go) choose every interleaving. A named
// point is the narrow form: one interleaving, the one a law names.
//
// Only a test build has points: the lowering writes no call of HookAt or
// HookPoint in a run build (lower hook.go), so a program's binary holds
// no hook code and no lookup on its paths. In a test build a point no
// test armed costs one atomic load and a branch: HookPoint inlines, and
// only an armed point takes the lock.

// hooks holds the armed points: by name, the functions the next task to
// reach the point runs, in the order they were armed; armed counts the
// names that hold some.
var hooks struct {
	armed atomic.Int32
	mu    sync.Mutex
	at    map[string][]func(*rt.Task)
}

// HookAt is testing.At(name, f): the next task that reaches Point(name)
// runs f on its own task, once. Arming a point twice runs both functions,
// in order, at the next reach. The points are the process's: a test that
// arms one is the only one that reaches it, or it says which reach it
// means by its name.
func HookAt(name string, f func(*rt.Task)) {
	hooks.mu.Lock()
	defer hooks.mu.Unlock()
	if hooks.at == nil {
		hooks.at = map[string][]func(*rt.Task){}
	}
	if len(hooks.at[name]) == 0 {
		hooks.armed.Add(1)
	}
	hooks.at[name] = append(hooks.at[name], f)
}

// HookPoint is testing.Point(name): the calling task t runs, in order,
// the functions armed at name since the last reach, and disarms them. It
// waits on nothing itself; a function it runs waits as its body does, on
// t. A point no test armed does nothing: while no point is armed it reads
// one counter.
func HookPoint(t *rt.Task, name string) {
	if hooks.armed.Load() != 0 {
		hookRun(t, name)
	}
}

// hookRun is HookPoint while some point is armed.
func hookRun(t *rt.Task, name string) {
	hooks.mu.Lock()
	armed := hooks.at[name]
	if len(armed) > 0 {
		delete(hooks.at, name)
		hooks.armed.Add(-1)
	}
	hooks.mu.Unlock()
	for _, f := range armed {
		f(t)
	}
}
