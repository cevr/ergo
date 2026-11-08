package unionreprbench

import "strconv"

// Counter is a machine whose unions all fit rule G's bound, so the rule
// picks a tagged form for each of them (round 2; R3's red check uses it):
//
//	type State = Idle | Counting           // Idle{}, Counting{ N int }
//	type Signal = Tick | Reset | Read      // all empty
//	func step(s State, sig Signal) Decision[State, int] {
//		match (s, sig) {
//		case (Idle, Tick):            return Enter{Counting{1}}
//		case (Counting as c, Tick):   return Stay{Counting{c.N + 1}}
//		case (Counting, Reset):       return Enter{Idle}
//		case (Idle, Reset):           return Ignore
//		case (Counting as c, Read):   return Done{c.N}
//		case (Idle, Read):            return Reject
//		}
//	}
//
// Form G (rule.go): State has the option shape, so it is C
// (struct{ v Counting; ok bool }, 16 bytes; Idle is the zero value);
// Signal is B (uint8); Decision[State, int] is D (tag, a State slot, an
// int64 slot: 32 bytes, 3 fields).

type Idle struct{}
type Counting struct{ N int64 }
type Tick struct{}
type Reset struct{}
type Read struct{}

// ---- Form A ----

type CStateA interface{ isCStateA() }
type CSignalA interface{ isCSignalA() }

func (Idle) isCStateA()     {}
func (Counting) isCStateA() {}
func (Tick) isCSignalA()    {}
func (Reset) isCSignalA()   {}
func (Read) isCSignalA()    {}

//go:noinline
func stepCA(s CStateA, sig CSignalA) DecisionA[CStateA, int64] {
	switch st := s.(type) {
	case Idle:
		switch sig.(type) {
		case Tick:
			return Enter[CStateA]{Counting{1}}
		case Reset:
			return Ignore{}
		case Read:
			return Reject{}
		}
	case Counting:
		switch sig.(type) {
		case Tick:
			return Stay[CStateA]{Counting{st.N + 1}}
		case Reset:
			return Enter[CStateA]{Idle{}}
		case Read:
			return Done[int64]{st.N}
		}
	}
	panic("ergo: defect: no case of the match matched")
}

//go:noinline
func applyCA(s CStateA, d DecisionA[CStateA, int64]) CStateA {
	switch v := d.(type) {
	case Enter[CStateA]:
		return v.Next
	case Stay[CStateA]:
		return v.Next
	case Ignore, Reject, Done[int64]:
		return s
	}
	panic("ergo: defect: no case of the match matched")
}

func textCStateA(s CStateA) string {
	switch v := s.(type) {
	case Idle:
		return "Idle"
	case Counting:
		return "Counting{" + strconv.FormatInt(v.N, 10) + "}"
	}
	panic("no variant")
}

func textCDecisionA(d DecisionA[CStateA, int64]) string {
	switch v := d.(type) {
	case Enter[CStateA]:
		return "Enter{" + textCStateA(v.Next) + "}"
	case Stay[CStateA]:
		return "Stay{" + textCStateA(v.Next) + "}"
	case Ignore:
		return "Ignore"
	case Reject:
		return "Reject"
	case Done[int64]:
		return "Done{" + strconv.FormatInt(v.Output, 10) + "}"
	}
	panic("no variant")
}

// ---- Form G (rule G): State C, Signal B, Decision D ----

// CStateC is form C: ok false is Idle, and then v is zero.
type CStateC struct {
	v  Counting
	ok bool
}

type CSignalB uint8

const (
	cTick CSignalB = 1 + iota
	cReset
	cRead
)

type CDecisionD struct {
	tag  uint8
	next CStateC
	out  int64
}

//go:noinline
func stepCG(s CStateC, sig CSignalB) CDecisionD {
	if !s.ok {
		switch sig {
		case cTick:
			return CDecisionD{tag: decEnter, next: CStateC{v: Counting{1}, ok: true}}
		case cReset:
			return CDecisionD{tag: decIgnore}
		case cRead:
			return CDecisionD{tag: decReject}
		}
	} else {
		switch sig {
		case cTick:
			return CDecisionD{tag: decStay, next: CStateC{v: Counting{s.v.N + 1}, ok: true}}
		case cReset:
			return CDecisionD{tag: decEnter, next: CStateC{}}
		case cRead:
			return CDecisionD{tag: decDone, out: s.v.N}
		}
	}
	panic("ergo: defect: no case of the match matched")
}

//go:noinline
func applyCG(s CStateC, d CDecisionD) CStateC {
	switch d.tag {
	case decEnter, decStay:
		return d.next
	case decIgnore, decReject, decDone:
		return s
	}
	panic("ergo: defect: no case of the match matched")
}

func (s CStateC) wellFormed() bool { return s.ok || s.v == Counting{} }

func (d CDecisionD) wellFormed() bool {
	switch d.tag {
	case decEnter, decStay:
		return d.next.wellFormed() && d.out == 0
	case decIgnore, decReject:
		return d.next == CStateC{} && d.out == 0
	case decDone:
		return d.next == CStateC{}
	}
	return false
}

func textCStateC(s CStateC) string {
	if !s.ok {
		return "Idle"
	}
	return "Counting{" + strconv.FormatInt(s.v.N, 10) + "}"
}

func textCDecisionD(d CDecisionD) string {
	switch d.tag {
	case decEnter:
		return "Enter{" + textCStateC(d.next) + "}"
	case decStay:
		return "Stay{" + textCStateC(d.next) + "}"
	case decIgnore:
		return "Ignore"
	case decReject:
		return "Reject"
	case decDone:
		return "Done{" + strconv.FormatInt(d.out, 10) + "}"
	}
	panic("no variant")
}

// counterSignals is a seeded sequence: Reset 2 in 1024, Read 158 in 1024,
// else Tick, so a count often passes 255 (form A's Counting{n} then
// allocates; Go keeps static boxes only for 0 to 255).
func counterSignals(n int, seed uint64) []CSignalB {
	r := newRand(seed)
	out := make([]CSignalB, n)
	for i := range out {
		switch x := r.next() % 1024; {
		case x < 2:
			out[i] = cReset
		case x < 160:
			out[i] = cRead
		default:
			out[i] = cTick
		}
	}
	return out
}

func counterSignalsA(raw []CSignalB) []CSignalA {
	out := make([]CSignalA, len(raw))
	for i, s := range raw {
		switch s {
		case cTick:
			out[i] = Tick{}
		case cReset:
			out[i] = Reset{}
		case cRead:
			out[i] = Read{}
		}
	}
	return out
}
