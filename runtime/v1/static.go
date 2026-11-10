package v1

import "ergo.local/prototype/runtime/rt"

// The static release of a frame (P-L8; resources R5, R9, R10, R21; plan
// "P-L8 release-speed"). A frame that opens its scope only for its owned
// values (NewOwnerScope), whose release plan is static (no drop flag, no
// generated release of parts, no
// reassignment of an owned local, no std call that holds a taken value),
// keeps its owned values in Go locals of their own and one bit set, not on
// the frame's list. Its one deferred function releases, last acquired
// first, each value whose bit is still set: on a return, a typed failure,
// a defect and a stop alike, each release after an earlier one's defect,
// every defect kept, and the exit settled as Scope.End settles it. A
// scope that ends inside the frame (a block, a loop iteration, a
// statement's temporaries) clears the bit and releases at once, as
// Owned.Release does.
//
// The list lowering (B1: NewOwnerScope, Owner, Owning, Hand, End) stays
// selectable as the reference (lower.releaseLowering = "scope"); a seeded
// property holds the two equal (lower: TestStaticReleaseAgrees).
//
// Prior art: Rust's drop elaboration writes a drop on each exit edge with
// a drop flag only where the plan is dynamic (MIR ElaborateDrops); Go's
// open-coded defers keep a bit per defer in a stack byte, as live does
// here; Swift's SIL lowers a destroy_addr on each exit edge.

// Static is the release of a static frame, made in its deferred function.
type Static struct {
	t     *rt.Task
	e     error
	exits rt.Cause
}

// StaticBegin starts the release of a static frame on task t: p is the
// frame's recover(), err its named error result (nil in a frame with no
// error row). A panic is a fresh defect, a carried exit or an interruption
// beside *err, as Scope.End records it.
func StaticBegin(t *rt.Task, err *error, p any) Static {
	s := Static{t: t}
	if err != nil {
		s.e = *err
	}
	if p != nil {
		s.e = join(s.e, capture(t, p))
	}
	return s
}

// Part runs one owned value's release; a defect joins the frame's exits
// and the releases after it still run (R9), as Scope.runOwn does.
func (s *Static) Part(rel func()) {
	defer func() {
		if p := recover(); p != nil {
			s.exits.Add(capture(s.t, p))
		}
	}()
	rel()
}

// Failed joins the releases so far into the frame's exit and reports
// whether the exit failed: the value the frame was returning (its kept
// bit, ReturnStatic) is then released, as Scope.settleKept does.
func (s *Static) Failed() bool {
	if len(s.exits.Reasons) > 0 {
		s.e = join(s.e, s.exits.Err())
		s.exits = rt.Cause{}
	}
	return s.e != nil
}

// End hands the frame's exit to it, as Scope.End does: a lone typed
// failure into *err, any other exit raised for the caller's boundary.
func (s *Static) End(err *error) {
	e := s.e
	if len(s.exits.Reasons) > 0 {
		e = join(e, s.exits.Err())
	}
	settle(err, e)
}

// OwnStatic registers v, the value of the owned slot whose bit is bit, in
// the step that receives it (R8): the slot holds v and its bit is set.
func OwnStatic[T any](live *uint64, bit uint64, slot *T, v T) T {
	*slot = v
	*live |= bit
	return v
}

// HandStatic is a hand-off of v, the value of the slot whose bit is bit:
// its owner no longer releases it (R10, R19).
func HandStatic[T any](live *uint64, bit uint64, v T) T {
	*live &^= bit
	return v
}

// ReturnStatic is the return of v, the value of the slot whose bit is bit
// (R10, Returned): its bit moves to kept, released only if the frame's
// exit fails.
func ReturnStatic[T any](live, kept *uint64, bit uint64, v T) T {
	*live &^= bit
	*kept |= bit
	return v
}
