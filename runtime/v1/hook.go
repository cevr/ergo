package v1

import (
	"sort"
	"strconv"
	"strings"
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
//
// A point armed and never reached must not leave its test waiting: a law
// whose server waits for the release the point's function gives would
// wait until the test's deadline. So once a point is armed, the run
// watches for a stall of the arming task's clock domain (rt stall.go:
// every task of that domain, and of the domains its tasks made, waits on
// a wait its clock sees, and no clock of them can move). A stall while
// the point is still armed is a defect that stops the run: "testing.At:
// point "name" was never reached". An arming task outside every clock's
// domain gets no watch.

// hooks holds the armed points: by name, the functions the next task to
// reach the point runs, in the order they were armed, and the domain of
// the task that armed it first; armed counts the names that hold some.
var hooks struct {
	armed atomic.Int32
	mu    sync.Mutex
	at    map[string]armedPoint
}

type armedPoint struct {
	fs     []func(*rt.Task)
	domain *rt.Virtual
}

// HookAt is testing.At(name, f), called by t: the next task that reaches
// Point(name) runs f on its own task, once. Arming a point twice runs both
// functions, in order, at the next reach. The points are the process's: a
// test that arms one is the only one that reaches it, or it says which
// reach it means by its name.
//
//ergo:wait none
func HookAt(t *rt.Task, name string, f func(*rt.Task)) {
	rt.WatchStalls(hookStalled)
	hooks.mu.Lock()
	defer hooks.mu.Unlock()
	if hooks.at == nil {
		hooks.at = map[string]armedPoint{}
	}
	p := hooks.at[name]
	if len(p.fs) == 0 {
		hooks.armed.Add(1)
		p.domain = rt.DomainOf(t)
	}
	p.fs = append(p.fs, f)
	hooks.at[name] = p
}

// HookPoint is testing.Point(name): the calling task t runs, in order,
// the functions armed at name since the last reach, and disarms them. It
// waits on nothing itself; a function it runs waits as its body does, on
// t. A point no test armed does nothing: while no point is armed it reads
// one counter.
//
//ergo:wait none
func HookPoint(t *rt.Task, name string) {
	if hooks.armed.Load() != 0 {
		hookRun(t, name)
	}
}

// hookRun is HookPoint while some point is armed.
func hookRun(t *rt.Task, name string) {
	hooks.mu.Lock()
	p := hooks.at[name]
	if len(p.fs) > 0 {
		delete(hooks.at, name)
		hooks.armed.Add(-1)
	}
	hooks.mu.Unlock()
	for _, f := range p.fs {
		f(t)
	}
}

// hookStalled is the stall watch's report (rt.WatchStalls), called after
// each park or leave of a domain's member: a point whose arming domain
// has stalled can be reached by no task any more, so the run stops with
// the defect that names it.
func hookStalled() {
	hooks.mu.Lock()
	var names []string
	for name, p := range hooks.at {
		if len(p.fs) > 0 && p.domain != nil && rt.Stalled(p.domain) {
			names = append(names, strconv.Quote(name))
		}
	}
	hooks.mu.Unlock()
	if len(names) == 0 {
		return
	}
	sort.Strings(names)
	hookDefect("testing.At: point " + strings.Join(names, ", ") + " was never reached: every task waits, and none can reach it")
}

// hookDefect stops the run with msg: a panic on the watch's goroutine,
// which no task recovers, so the test binary exits 2 with the message.
// Only TestUnreachedPointStopsTheRun replaces it, to see the message
// instead of the process's end.
var hookDefect = func(msg string) { panic(msg) }
