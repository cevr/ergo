//go:build ergoexplore

package v1

import (
	"fmt"
	"slices"
	"sync"

	"ergo.local/prototype/runtime/rt"
)

// The children laws of an explored run (runtime.md, Explored schedules;
// Machines and actors 14). Every explored run is traced, and childLaws
// reads its machine events: the invariants of the design model
// models/children (X3), checked on the real runner under each schedule
// (X7). The first event that breaks one names the law, and the run fails
// with that text.
//
//   - ChildInsideEntry: a child lives only inside the entry that started
//     it: it starts while that entry is open, and it has ended when the
//     entry's work is interrupted and when the parent opens its next
//     entry.
//   - NoLateChildOutput: the parent steps no output of an ended entry: at
//     each child output it steps, the children of the open entry have
//     given at least as many outputs as it stepped in that entry.
//   - ChildrenStopFirst: no child lives after its parent ended, and a
//     child that ends while its entry ends has no live sibling started
//     after it (the last started stops first).
//
// A child is found from the task tree: the first ancestor of its runner
// that is a work names the parent and the entry (the work's epoch). An
// entry ends at the parent's decision that leaves it (Enter, Emit, Done),
// its failure or its stop, and the next entry opens at the parent's first
// event in the new epoch after that decision.
type childLaws struct {
	mu     sync.Mutex
	parent map[uint64]uint64    // task -> parent task, from start events
	actors map[uint64]*lawActor // runner task -> actor
	works  map[uint64]lawEntry  // work task -> its entry
	out    map[lawEntry]int     // an entry's outputs its children gave
	taken  map[lawEntry]int     // an entry's child outputs its parent stepped
	broken string
}

// lawEntry is an entry of an actor: its runner task and its epoch.
type lawEntry struct {
	actor uint64
	epoch uint64
}

type lawActor struct {
	task   uint64
	name   string
	open   uint64 // the epoch of the open entry
	ending bool   // the open entry's exit began
	live   bool
	owner  *lawEntry           // the entry that started it; nil for a root
	kids   map[uint64][]uint64 // epoch -> the children it started, in order
}

func newChildLaws() *childLaws {
	return &childLaws{parent: map[uint64]uint64{}, actors: map[uint64]*lawActor{}, works: map[uint64]lawEntry{},
		out: map[lawEntry]int{}, taken: map[lawEntry]int{}}
}

// verdict is the first law the run broke, "" when none.
func (l *childLaws) verdict() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.broken
}

func (l *childLaws) breaks(law, format string, args ...any) {
	if l.broken == "" {
		l.broken = "explore: law " + law + " broken: " + fmt.Sprintf(format, args...)
	}
}

// Event is rt.Tracer's.
func (l *childLaws) Event(e rt.Event) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if e.Kind == rt.TraceStart {
		l.parent[e.Task] = e.Parent
		return
	}
	m := e.Machine()
	if m == nil {
		return
	}
	switch m.What {
	case rt.MachineWorkStarted:
		if a := l.actorAbove(e.Task); a != nil {
			l.works[e.Task] = lawEntry{a.task, m.Epoch}
			l.opens(a, m.Epoch)
		}
		return
	case rt.MachineWorkInterrupted, rt.MachineWorkFailed:
		if en, ok := l.works[e.Task]; ok {
			if k := l.liveKid(en); k != nil {
				l.breaks("ChildInsideEntry", "the work of %s's entry %d ended (%s) while its child %s lives", l.actors[en.actor].name, en.epoch, m.What, k.name)
			}
		}
		return
	case rt.MachineStart:
		l.start(e)
		return
	}
	a := l.actors[e.Task]
	if a == nil {
		return
	}
	if m.What != rt.MachineDecide && m.What != rt.MachineStop && m.What != rt.MachineExit && m.Epoch > a.open {
		l.opens(a, m.Epoch)
	}
	switch m.What {
	case rt.MachineSignal:
		if m.Source == "child" {
			en := lawEntry{a.task, a.open}
			l.taken[en]++
			if l.taken[en] > l.out[en] {
				l.breaks("NoLateChildOutput", "%s stepped child output %d in entry %d, whose children gave %d", a.name, l.taken[en], a.open, l.out[en])
			}
		}
	case rt.MachineDecide:
		switch m.Decision {
		case "Emit", "Done":
			if a.owner != nil {
				l.out[*a.owner]++
			}
		}
		switch m.Decision {
		case "Enter", "Emit", "Done":
			a.ending = true
		}
	case rt.MachineFail, rt.MachineStop:
		a.ending = true
	case rt.MachineExit:
		l.exit(a)
	}
}

// start records an actor's start; a child also starts inside an open
// entry of its parent.
func (l *childLaws) start(e rt.Event) {
	a := &lawActor{task: e.Task, name: fmt.Sprintf("actor %d", len(l.actors)), live: true, kids: map[uint64][]uint64{}}
	l.actors[e.Task] = a
	for x := l.parent[e.Task]; x != 0; x = l.parent[x] {
		en, ok := l.works[x]
		if !ok {
			continue
		}
		p := l.actors[en.actor]
		a.owner = &en
		p.kids[en.epoch] = append(p.kids[en.epoch], a.task)
		a.name = fmt.Sprintf("%s (child %d of %s's entry %d)", a.name, len(p.kids[en.epoch]), p.name, en.epoch)
		if !p.live || p.open != en.epoch {
			l.breaks("ChildInsideEntry", "%s started outside its entry (%s is in entry %d, live %v)", a.name, p.name, p.open, p.live)
		}
		return
	}
}

// opens opens a's entry epoch: the entries before it have no live child.
func (l *childLaws) opens(a *lawActor, epoch uint64) {
	if epoch <= a.open {
		return
	}
	for e := range a.kids {
		if e >= epoch {
			continue
		}
		if k := l.liveKid(lawEntry{a.task, e}); k != nil {
			l.breaks("ChildInsideEntry", "%s opened entry %d while %s lives", a.name, epoch, k.name)
		}
	}
	a.open, a.ending = epoch, false
}

// exit ends a: a child that ends with its entry ending has no live
// younger sibling, and an ended actor has no live child.
func (l *childLaws) exit(a *lawActor) {
	a.live = false
	if a.owner != nil {
		p := l.actors[a.owner.actor]
		if p.ending || !p.live || p.open != a.owner.epoch {
			sibs := p.kids[a.owner.epoch]
			for _, s := range sibs[slices.Index(sibs, a.task)+1:] {
				if k := l.actors[s]; k.live {
					l.breaks("ChildrenStopFirst", "%s ended before %s, started after it", a.name, k.name)
				}
			}
		}
	}
	for e := range a.kids {
		if k := l.liveKid(lawEntry{a.task, e}); k != nil {
			l.breaks("ChildrenStopFirst", "%s ended while %s lives", a.name, k.name)
		}
	}
}

// liveKid is a live child of the entry en, nil when none is.
func (l *childLaws) liveKid(en lawEntry) *lawActor {
	a := l.actors[en.actor]
	if a == nil {
		return nil
	}
	for _, k := range a.kids[en.epoch] {
		if c := l.actors[k]; c.live {
			return c
		}
	}
	return nil
}

// actorAbove is the actor whose runner is task or its nearest ancestor.
func (l *childLaws) actorAbove(task uint64) *lawActor {
	for x := task; x != 0; x = l.parent[x] {
		if a := l.actors[x]; a != nil {
			return a
		}
	}
	return nil
}
