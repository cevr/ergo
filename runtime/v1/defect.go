package v1

import (
	"sync/atomic"

	"ergo.local/prototype/runtime/rt"
)

var defectSeq atomic.Uint64

func nextDefectOccurrence() uint64 { return defectSeq.Add(1) }

// SourceDefect is the deliberate defect raised by Ergo's `die <expr>` statement
// (NO-UNREACHABLE §3). It is allocated only on the cold exit edge and preserves
// the exact nominal Go payload value without stringifying or serializing it.
type SourceDefect struct {
	Payload    any
	At         string
	Occurrence uint64
}

// String is the defect's diagnostic text, written after "panic: " in reports.
func (d *SourceDefect) String() string {
	if d == nil {
		return "die"
	}
	if d.At == "" {
		return "die: " + rt.Text(d.Payload)
	}
	return "die at " + d.At + ": " + rt.Text(d.Payload)
}

// DieAt constructs the SourceDefect raised by `die <expr>` at source position
// at. Lowered code emits panic(ergo.DieAt(val, "file:line:col")), keeping the
// success path free of any overhead.
func DieAt(payload any, at string) *SourceDefect {
	return &SourceDefect{
		Payload:    payload,
		At:         at,
		Occurrence: nextDefectOccurrence(),
	}
}

// Defect is std task's opaque read-only view of a task's or actor's defect
// cause (NO-UNREACHABLE §3). It preserves the whole runtime *rt.Cause across
// joins, cleanup releases, and interruptions, while exposing position,
// occurrence, cause counts, and total nominal payload lookup (DefectHas/DefectAs).
type Defect struct {
	cause *rt.Cause
	text  string
}

func newDefect(err error) *Defect {
	d := &Defect{text: defectReason(err)}
	if c, ok := err.(*rt.Cause); ok && c != nil {
		d.cause = c
	}
	return d
}

// firstDefectReason returns the first KindDefect reason in d.cause, if any.
func (d *Defect) firstDefectReason() (rt.Reason, bool) {
	if d == nil || d.cause == nil {
		return rt.Reason{}, false
	}
	for _, r := range d.cause.Reasons {
		if r.Kind == rt.KindDefect {
			return r, true
		}
	}
	return rt.Reason{}, false
}

// firstSourceDefect unwraps the first KindDefect's value as a *SourceDefect.
// Foreign panics, runtime faults, and legacy UnreachableDefect return nil so
// ReasonAs never fabricates a typed payload for a foreign defect.
func (d *Defect) firstSourceDefect() *SourceDefect {
	r, ok := d.firstDefectReason()
	if !ok {
		return nil
	}
	v := r.Value
	if p, ok := v.(*Panic); ok && p != nil {
		v = p.Value
	}
	sd, _ := v.(*SourceDefect)
	return sd
}

// Text is the rendered text of the first defect, matching Defected.Reason.
//
//ergo:wait none
func (d *Defect) Text() string {
	if d == nil {
		return ""
	}
	return d.text
}

// Position is the source position ("file:line:col") of the first deliberate
// defect, or "" for a foreign/runtime defect with no source die site.
//
//ergo:wait none
func (d *Defect) Position() string {
	r, ok := d.firstDefectReason()
	if !ok {
		return ""
	}
	v := r.Value
	if p, ok := v.(*Panic); ok && p != nil {
		v = p.Value
	}
	switch x := v.(type) {
	case *SourceDefect:
		if x != nil {
			return x.At
		}
	case *UnreachableDefect:
		if x != nil {
			return x.At
		}
	}
	return ""
}

// Occurrence is the monotonic occurrence id of the first defect in the cause.
//
//ergo:wait none
func (d *Defect) Occurrence() int {
	r, ok := d.firstDefectReason()
	if !ok {
		return 0
	}
	if p, ok := r.Value.(*Panic); ok && p != nil {
		if p.Occurrence != 0 {
			return int(p.Occurrence)
		}
		if sd, ok := p.Value.(*SourceDefect); ok && sd != nil {
			return int(sd.Occurrence)
		}
	}
	if sd, ok := r.Value.(*SourceDefect); ok && sd != nil {
		return int(sd.Occurrence)
	}
	return 0
}

// CauseCount is the total number of reasons recorded in the task's exit cause
// (body defects, cleanup/release defects, interruptions, typed failures).
//
//ergo:wait none
func (d *Defect) CauseCount() int {
	if d == nil || d.cause == nil {
		return 0
	}
	return len(d.cause.Reasons)
}

// DefectCount is the number of defect reasons recorded in the task's exit cause.
//
//ergo:wait none
func (d *Defect) DefectCount() int {
	if d == nil || d.cause == nil {
		return 0
	}
	n := 0
	for _, r := range d.cause.Reasons {
		if r.Kind == rt.KindDefect {
			n++
		}
	}
	return n
}

// DefectHas reports whether d's first defect is a deliberate source defect
// carrying a payload of the exact nominal Go type T.
//
//ergo:wait none
func DefectHas[T any](d *Defect) bool {
	sd := d.firstSourceDefect()
	if sd == nil {
		return false
	}
	_, ok := sd.Payload.(T)
	return ok
}

// DefectAs returns the payload of d's first deliberate source defect at type T,
// or the zero value of T when DefectHas[T](d) is false.
//
//ergo:wait none
func DefectAs[T any](d *Defect) T {
	sd := d.firstSourceDefect()
	if sd != nil {
		if v, ok := sd.Payload.(T); ok {
			return v
		}
	}
	var zero T
	return zero
}
