//go:build ergoexplore

package v1

import (
	"slices"
	"strconv"
	"sync"

	"ergo.local/prototype/runtime/rt"
)

// Model-driven runtime trace conformance against models/children (MODELCHECK E4;
// runtime.md, Explored schedules; Machines and actors 14). Every explored run
// is traced (rt.ExploreTraced) and childrenConformer checks its rt.TraceMachine
// stream against the state machine and invariants of models/children/children.ergo
// using candidate-set conformance tracking (as std/explore.Conform does):
//
//   - ChildInsideEntry: every live child's owner is live and in the entry epoch
//     that started the child; an entry's work ends/interrupts and the next entry
//     opens only after all of that entry's children have ended.
//   - NoLateChildOutput: a parent steps a child output (Take) only when a fresh
//     output of the open entry is in its inbox; stale outputs of ended entries
//     are dropped (DropStale), never stepped.
//   - ChildrenStopFirst: no child lives after its parent ends, and a child
//     ending while its owner entry closes has no live younger sibling.
//
// Events the model has no signal for are never silently ignored: an unmapped
// rt.MachineWhat or an empty candidate set produces a conformance mismatch
// naming the broken models/children law.

type childOut struct {
	from  int
	epoch uint64
}

// childrenWorld is the state of models/children.World extended to arbitrary
// actor counts, entry epochs, and inbox depths for live runtime traces.
type childrenWorld struct {
	alive      []bool
	owner      []int
	ownerEpoch []uint64
	epoch      []uint64
	kids       [][]int
	closing    []int // 0 open, 1 reentering, 2 ending
	inbox      [][]childOut
}

func (w childrenWorld) clone() childrenWorld {
	out := childrenWorld{
		alive:      slices.Clone(w.alive),
		owner:      slices.Clone(w.owner),
		ownerEpoch: slices.Clone(w.ownerEpoch),
		epoch:      slices.Clone(w.epoch),
		kids:       make([][]int, len(w.kids)),
		closing:    slices.Clone(w.closing),
		inbox:      make([][]childOut, len(w.inbox)),
	}
	for i := range w.kids {
		out.kids[i] = slices.Clone(w.kids[i])
	}
	for i := range w.inbox {
		out.inbox[i] = slices.Clone(w.inbox[i])
	}
	return out
}

func (w *childrenWorld) addNode(owner int, ownerEpoch uint64) int {
	id := len(w.alive)
	w.alive = append(w.alive, true)
	w.owner = append(w.owner, owner)
	w.ownerEpoch = append(w.ownerEpoch, ownerEpoch)
	w.epoch = append(w.epoch, 0)
	w.kids = append(w.kids, nil)
	w.closing = append(w.closing, 0)
	w.inbox = append(w.inbox, nil)
	if owner >= 0 {
		w.kids[owner] = append(w.kids[owner], id)
	}
	return id
}

func (w childrenWorld) liveKid(actor int, epoch uint64) int {
	for _, c := range w.kids[actor] {
		if w.alive[c] && w.ownerEpoch[c] == epoch {
			return c
		}
	}
	return -1
}

func (w childrenWorld) anyLiveKid(actor int) int {
	for _, c := range w.kids[actor] {
		if w.alive[c] {
			return c
		}
	}
	return -1
}

type childrenActKind uint8

const (
	actStartRoot childrenActKind = iota + 1
	actStartChild
	actEmit
	actTake
	actDropStale
	actReenter
	actSelfEnd
	actEndNode
	actOpen
	actWorkStop
)

type childrenAct struct {
	kind   childrenActKind
	actor  int
	parent int
	epoch  uint64
	detail string
}

// stepChildrenModel executes one transition of models/children on w and returns
// the next conforming world, or (zero, law, reason) if the transition violates
// a models/children invariant.
func stepChildrenModel(w childrenWorld, act childrenAct) (childrenWorld, bool, string, string) {
	next := w.clone()
	switch act.kind {
	case actStartRoot:
		next.addNode(-1, 0)
		return next, true, "", ""

	case actStartChild:
		p := act.parent
		if p < 0 || p >= len(next.alive) {
			return childrenWorld{}, false, "ChildInsideEntry", "child started with unknown parent"
		}
		if !next.alive[p] || next.epoch[p] != act.epoch {
			return childrenWorld{}, false, "ChildInsideEntry", "child started outside its entry (parent in entry " + uintText(next.epoch[p]) + ", live " + strconv.FormatBool(next.alive[p]) + ")"
		}
		next.addNode(p, act.epoch)
		return next, true, "", ""

	case actEmit:
		c := act.actor
		if c < 0 || c >= len(next.alive) || !next.alive[c] {
			return childrenWorld{}, false, "NoLateChildOutput", "ended child emitted output"
		}
		p := next.owner[c]
		if p >= 0 && next.alive[p] {
			next.inbox[p] = append(next.inbox[p], childOut{from: c, epoch: next.ownerEpoch[c]})
		}
		return next, true, "", ""

	case actTake:
		p := act.actor
		if p < 0 || p >= len(next.alive) || !next.alive[p] {
			return childrenWorld{}, false, "NoLateChildOutput", "ended actor stepped child output"
		}
		idx := -1
		for i, o := range next.inbox[p] {
			if o.epoch == next.epoch[p] {
				idx = i
				break
			}
		}
		if idx < 0 {
			return childrenWorld{}, false, "NoLateChildOutput", "actor stepped child output in entry " + uintText(next.epoch[p]) + " with no fresh child output in inbox"
		}
		next.inbox[p] = slices.Delete(next.inbox[p], idx, idx+1)
		return next, true, "", ""

	case actDropStale:
		p := act.actor
		if p < 0 || p >= len(next.alive) {
			return childrenWorld{}, false, "NoLateChildOutput", "unknown actor dropped stale child output"
		}
		idx := -1
		for i, o := range next.inbox[p] {
			if o.epoch == act.epoch && o.epoch != next.epoch[p] {
				idx = i
				break
			}
		}
		if idx < 0 {
			for i, o := range next.inbox[p] {
				if o.epoch != next.epoch[p] {
					idx = i
					break
				}
			}
		}
		if idx >= 0 {
			next.inbox[p] = slices.Delete(next.inbox[p], idx, idx+1)
		}
		return next, true, "", ""

	case actReenter:
		p := act.actor
		if p < 0 || p >= len(next.alive) || !next.alive[p] {
			return next, true, "", ""
		}
		if next.closing[p] == 0 {
			next.closing[p] = 1
		}
		return next, true, "", ""

	case actSelfEnd:
		p := act.actor
		if p < 0 || p >= len(next.alive) {
			return next, true, "", ""
		}
		next.closing[p] = 2
		return next, true, "", ""

	case actOpen:
		p := act.actor
		if p < 0 || p >= len(next.alive) || !next.alive[p] {
			return childrenWorld{}, false, "ChildInsideEntry", "ended actor opened entry " + uintText(act.epoch)
		}
		if act.epoch <= next.epoch[p] {
			return next, true, "", ""
		}
		for _, c := range next.kids[p] {
			if next.alive[c] && next.ownerEpoch[c] < act.epoch {
				return childrenWorld{}, false, "ChildInsideEntry", "actor opened entry " + uintText(act.epoch) + " while child " + strconv.Itoa(c) + " of entry " + uintText(next.ownerEpoch[c]) + " lives"
			}
		}
		next.epoch[p] = act.epoch
		next.closing[p] = 0
		return next, true, "", ""

	case actWorkStop:
		p := act.actor
		if p < 0 || p >= len(next.alive) {
			return next, true, "", ""
		}
		if k := next.liveKid(p, act.epoch); k >= 0 {
			return childrenWorld{}, false, "ChildInsideEntry", "work of entry " + uintText(act.epoch) + " ended (" + act.detail + ") while child " + strconv.Itoa(k) + " lives"
		}
		return next, true, "", ""

	case actEndNode:
		n := act.actor
		if n < 0 || n >= len(next.alive) {
			return childrenWorld{}, false, "ChildrenStopFirst", "unknown actor ended"
		}
		if p := next.owner[n]; p >= 0 {
			if next.closing[p] != 0 || !next.alive[p] || next.epoch[p] != next.ownerEpoch[n] {
				sibs := next.kids[p]
				pos := slices.Index(sibs, n)
				if pos >= 0 {
					for _, s := range sibs[pos+1:] {
						if next.alive[s] && next.ownerEpoch[s] == next.ownerEpoch[n] {
							return childrenWorld{}, false, "ChildrenStopFirst", "child " + strconv.Itoa(n) + " ended before younger sibling " + strconv.Itoa(s)
						}
					}
				}
			}
		}
		if k := next.anyLiveKid(n); k >= 0 {
			return childrenWorld{}, false, "ChildrenStopFirst", "actor ended while child " + strconv.Itoa(k) + " lives"
		}
		next.alive[n] = false
		next.inbox[n] = nil
		return next, true, "", ""
	}
	return childrenWorld{}, false, "UnmappedAction", "unrecognized children model action"
}

type workEntry struct {
	actor int
	epoch uint64
}

// childrenConformer checks a live rt.TraceMachine event stream against
// models/children using candidate-set conformance (explore.Conform semantics).
type childrenConformer struct {
	mu         sync.Mutex
	parent     map[uint64]uint64
	actorID    map[uint64]int
	actorNames map[int]string
	works      map[uint64]workEntry
	candidates []childrenWorld
	mismatch   string
}

func newChildrenConformer() *childrenConformer {
	return &childrenConformer{
		parent:     map[uint64]uint64{},
		actorID:    map[uint64]int{},
		actorNames: map[int]string{},
		works:      map[uint64]workEntry{},
		candidates: []childrenWorld{{}},
	}
}

// verdict returns the first conformance mismatch, "" when the trace conforms.
func (c *childrenConformer) verdict() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.mismatch
}

func (c *childrenConformer) fail(law, reason string) {
	if c.mismatch == "" {
		if law != "" {
			c.mismatch = "explore: models/children conformance mismatch (law " + law + " broken): " + reason
		} else {
			c.mismatch = "explore: models/children conformance mismatch: " + reason
		}
	}
}

func (c *childrenConformer) apply(act childrenAct) {
	if c.mismatch != "" {
		return
	}
	var next []childrenWorld
	var firstLaw, firstReason string
	for _, w := range c.candidates {
		nw, ok, law, reason := stepChildrenModel(w, act)
		if ok {
			next = append(next, nw)
		} else if firstReason == "" {
			firstLaw, firstReason = law, reason
		}
	}
	if len(next) == 0 {
		c.fail(firstLaw, firstReason)
		return
	}
	c.candidates = next
}

func uintText(n uint64) string { return strconv.FormatUint(n, 10) }

// Event implements rt.Tracer: maps every rt.TraceMachine event strictly into
// models/children actions and rejects any unmapped rt.MachineWhat.
func (c *childrenConformer) Event(e rt.Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e.Kind == rt.TraceStart {
		c.parent[e.Task] = e.Parent
		return
	}
	m := e.Machine()
	if m == nil {
		return
	}
	switch m.What {
	case rt.MachineWorkStarted:
		if a, ok := c.actorAbove(e.Task); ok {
			c.works[e.Task] = workEntry{actor: a, epoch: m.Epoch}
			c.maybeOpen(a, m.Epoch)
		}
		return

	case rt.MachineWorkEnded:
		return

	case rt.MachineWorkInterrupted, rt.MachineWorkFailed:
		if en, ok := c.works[e.Task]; ok {
			c.apply(childrenAct{kind: actWorkStop, actor: en.actor, epoch: en.epoch, detail: m.What.String()})
		}
		return

	case rt.MachineStart:
		c.handleStart(e)
		return

	case rt.MachineChildStarted, rt.MachineChildStopped:
		return

	case rt.MachineSignal, rt.MachineTimerFired, rt.MachineTimerArmed, rt.MachineDropped, rt.MachineDecide, rt.MachineFail, rt.MachineStop, rt.MachineExit:
		a, ok := c.actorID[e.Task]
		if !ok {
			return
		}
		if m.What != rt.MachineDecide && m.What != rt.MachineStop && m.What != rt.MachineExit && m.What != rt.MachineDropped {
			c.maybeOpen(a, m.Epoch)
		}
		switch m.What {
		case rt.MachineSignal:
			if m.Source == "child" {
				c.apply(childrenAct{kind: actTake, actor: a, epoch: m.Epoch})
			}
		case rt.MachineDropped:
			if m.Source == "child" {
				c.apply(childrenAct{kind: actDropStale, actor: a, epoch: m.Epoch})
			}
		case rt.MachineDecide:
			switch m.Decision {
			case "Emit", "Tell", "Done":
				c.apply(childrenAct{kind: actEmit, actor: a})
			}
			switch m.Decision {
			case "Enter", "Emit", "Done":
				c.apply(childrenAct{kind: actReenter, actor: a})
			}
		case rt.MachineFail, rt.MachineStop:
			c.apply(childrenAct{kind: actSelfEnd, actor: a})
		case rt.MachineExit:
			c.apply(childrenAct{kind: actEndNode, actor: a})
		}
		return

	default:
		c.fail("UnmappedEvent", "unmapped rt.MachineWhat "+m.What.String()+" ("+strconv.Itoa(int(m.What))+")")
	}
}

func (c *childrenConformer) maybeOpen(actor int, epoch uint64) {
	if len(c.candidates) == 0 || actor >= len(c.candidates[0].epoch) {
		return
	}
	if epoch > c.candidates[0].epoch[actor] {
		c.apply(childrenAct{kind: actOpen, actor: actor, epoch: epoch})
	}
}

func (c *childrenConformer) handleStart(e rt.Event) {
	id := len(c.actorID)
	c.actorID[e.Task] = id
	c.actorNames[id] = "actor " + strconv.Itoa(id)
	for x := c.parent[e.Task]; x != 0; x = c.parent[x] {
		en, ok := c.works[x]
		if !ok {
			continue
		}
		c.apply(childrenAct{kind: actStartChild, actor: id, parent: en.actor, epoch: en.epoch})
		return
	}
	c.apply(childrenAct{kind: actStartRoot, actor: id})
}

func (c *childrenConformer) actorAbove(task uint64) (int, bool) {
	for x := task; x != 0; x = c.parent[x] {
		if a, ok := c.actorID[x]; ok {
			return a, true
		}
	}
	return 0, false
}
