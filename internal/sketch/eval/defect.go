package eval

import (
	"fmt"
	"sync/atomic"

	"ergo.local/prototype/internal/sketch/syntax"
)

var evalDefectSeq atomic.Int64

func nextEvalDefectOccurrence() int64 { return evalDefectSeq.Add(1) }

// defectV is the evaluator's representation of task.Defect (runtime/v1.Defect):
// an opaque read-only view of a task's or actor's defect cause, preserving
// nominal payload type identity, source position, monotonic occurrence id, and
// cause/defect counts across cleanup releases and interruptions.
type defectV struct {
	text        string
	pos         string
	occurrence  int64
	causes      int64
	defects     int64
	hasPayload  bool
	payload     Value
	payloadType rtype
}

func (d *defectV) describe() string { return fmt.Sprintf("a Defect (%s)", d.text) }

func (d *defectV) opCall(in *interp, fr *frame, at syntax.Node, name string, args []Value) (Value, *abrupt) {
	if ab := in.charge(fr, at, 1); ab != nil {
		return nil, ab
	}
	if d == nil {
		switch name {
		case "Text", "Position":
			return "", nil
		case "Occurrence", "CauseCount", "DefectCount":
			return int64(0), nil
		}
	}
	switch name {
	case "Text":
		return d.text, nil
	case "Position":
		return d.pos, nil
	case "Occurrence":
		return d.occurrence, nil
	case "CauseCount":
		return d.causes, nil
	case "DefectCount":
		return d.defects, nil
	}
	ice("unknown Defect operation %s", name)
	return nil, nil
}

// defectOfAbrupt builds or returns the defectV for an abrupt defect exit.
func defectOfAbrupt(ab *abrupt) *defectV {
	if ab == nil {
		return &defectV{}
	}
	if ab.defect != nil {
		return ab.defect
	}
	d := &defectV{
		text:       ab.msg,
		occurrence: nextEvalDefectOccurrence(),
		causes:     1,
		defects:    1,
	}
	ab.defect = d
	return d
}

// joinDefect merges a secondary release/cleanup defect into the primary exit,
// preserving the first defect's payload, position, and occurrence while keeping
// the combined cause and defect counts (matching runtime/rt.Cause).
func joinDefect(primary, extra *abrupt) {
	if primary == nil || extra == nil {
		return
	}
	p := defectOfAbrupt(primary)
	e := defectOfAbrupt(extra)
	p.causes += e.causes
	p.defects += e.defects
}

// dieStmt evaluates `die <expr>` once under ordinary failure/needs/ownership
// rules, then raises a structured deliberate defect carrying the owned payload
// and exact checked nominal type.
func (in *interp) dieStmt(fr *frame, s *syntax.DieStmt) *abrupt {
	v, ab := in.expr(fr, s.X)
	if ab != nil {
		return ab
	}
	t := fr.rtypeOf(s.X)
	site := in.site(fr.pkg, fr.decl, s.Pos())
	pos := fmt.Sprintf("%s:%d:%d", site.File, site.Line, site.Column)
	msg := fmt.Sprintf("die at %s: %v", pos, v)
	d := &defectV{
		text:        msg,
		pos:         pos,
		occurrence:  nextEvalDefectOccurrence(),
		causes:      1,
		defects:     1,
		hasPayload:  true,
		payload:     v,
		payloadType: t,
	}
	out := fr.abrupt(ctlDefect, s, nil, msg)
	out.defect = d
	return out
}

// defectCallTypeArg resolves the single generic type argument T of
// task.defectHas[T] / task.defectAs[T] in the caller's frame.
func defectCallTypeArg(fr *frame, at syntax.Node) (rtype, bool) {
	call, ok := at.(*syntax.CallExpr)
	if !ok || fr.info == nil {
		return rtype{}, false
	}
	ci := fr.info.Calls[call]
	if ci == nil || len(ci.TypeArgs) != 1 {
		return rtype{}, false
	}
	return fr.rt(ci.TypeArgs[0]), true
}

func defectMatches(d *defectV, want rtype) bool {
	if d == nil || !d.hasPayload {
		return false
	}
	return typeKey(d.payloadType) == typeKey(want)
}
