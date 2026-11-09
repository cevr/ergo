package v1

import (
	"slices"
	"testing"
	"time"

	"ergo.local/prototype/runtime/rt"
)

// TestActorTracesChildren: a traced actor whose work starts a child
// reports child-started with the entry's epoch once the child is adopted,
// and child-stopped with the same epoch once the parent's stop has halted
// it, before the parent's exit. Mutants: the adopt hook reports no
// child-started; the wrapped halt reports child-stopped before the child
// ended or not at all.
func TestActorTracesChildren(t *testing.T) {
	log := &machineLog{}
	root := rt.TracedRoot("child trace", log)
	sl := releaser{make(chan sleep)}
	noTimer := func(int) time.Duration { return ActorNoTimer() }
	kind := func(d int) int { return d }
	next := func(s, _ int) int { return s }
	ignore := func(*rt.Task, int, int) (int, error) { return decideIgnore, nil }
	started := make(chan struct{})
	parent := MachineStart(Site("parent"), root, sl, 0, ignore, nil, noTimer,
		func(s int) bool { return s == 0 }, func(w bool) bool { return w },
		func(wt *rt.Task, _ bool, _ func(int)) {
			_ = MachineChild(Site("child"), wt, sl, 0, ignore, nil, noTimer,
				noWork[int], hasNoWork, runNoWork[int], kind, next, func(*rt.Task, int, func(int)) {}, 1)
			close(started)
			<-wt.Done()
			panic(rt.Interrupted)
		},
		kind, next, func(*rt.Task, int) {}, 1)
	<-started
	if err := parent.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	log.mu.Lock()
	evs := slices.Clone(log.evs)
	log.mu.Unlock()
	slices.SortFunc(evs, func(a, b rt.Event) int { return int(a.ID) - int(b.ID) })
	var whats []rt.MachineWhat
	var epoch uint64
	startEpoch, sawStart := uint64(0), false
	for _, e := range evs {
		m := e.Machine()
		switch m.What {
		case rt.MachineStart:
			if !sawStart {
				startEpoch, sawStart = m.Epoch, true
			}
		case rt.MachineChildStarted, rt.MachineChildStopped:
			epoch = m.Epoch
		default:
			if m.What != rt.MachineExit {
				continue
			}
		}
		whats = append(whats, m.What)
	}
	if epoch != startEpoch {
		t.Fatalf("child events carry epoch %d, want the entry's %d", epoch, startEpoch)
	}
	// The parent starts first; the child's runner start and child-started
	// may come in either order, since adopt reports once start returned.
	// Then the child's exit, child-stopped from the halt (once the child
	// ended), and the parent's exit, last.
	if len(whats) >= 3 && whats[1] == rt.MachineStart && whats[2] == rt.MachineChildStarted {
		whats[1], whats[2] = whats[2], whats[1]
	}
	want := []rt.MachineWhat{rt.MachineStart, rt.MachineChildStarted, rt.MachineStart,
		rt.MachineExit, rt.MachineChildStopped, rt.MachineExit}
	if !slices.Equal(whats, want) {
		t.Fatalf("events %v, want %v", whats, want)
	}
}
