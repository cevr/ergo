package unionreprbench

import "strconv"

// The Breaker machine of check/testdata/fixtures/positive/machine-step-total.ergo,
// in its pure form (no Probe capability): an open breaker that gets Retry
// enters Closed{0}.
//
//	type State = Closed | Open
//	type Signal = Failure | Retry
//	func step(s State, sig Signal) Decision[State, string] {
//		match (s, sig) {
//		case (Closed as st, Failure as e):
//			if st.Failures >= 2 { return Enter{Open{e.Endpoint}} }
//			return Stay{Closed{st.Failures + 1}}
//		case (Open, Retry):   return Enter{Closed{0}}
//		case (Closed, Retry): return Ignore
//		case (Open, Failure): return Done{"open"}
//		}
//	}
//
// The variant records are shared by every form: a form changes only how a
// union value holds them.

type Closed struct{ Failures int64 }
type Open struct{ Endpoint string }
type Failure struct{ Endpoint string }
type Retry struct{}

// The prelude's Decision variants (internal/sketch/lower emits them per
// program).
type Enter[S any] struct{ Next S }
type Stay[S any] struct{ Next S }
type Ignore struct{}
type Reject struct{}
type Done[O any] struct{ Output O }

// ---- Form A: sealed interfaces (today's lowering) ----

type StateA interface{ isStateA() }
type SignalA interface{ isSignalA() }
type DecisionA[S any, O any] interface{ isDecisionA() }

func (Closed) isStateA()      {}
func (Open) isStateA()        {}
func (Failure) isSignalA()    {}
func (Retry) isSignalA()      {}
func (Enter[_]) isDecisionA() {}
func (Stay[_]) isDecisionA()  {}
func (Ignore) isDecisionA()   {}
func (Reject) isDecisionA()   {}
func (Done[_]) isDecisionA()  {}

// stepA is the nested type switch a Go programmer writes for a sealed
// interface (M1's target shape).
//
//go:noinline
func stepA(s StateA, sig SignalA) DecisionA[StateA, string] {
	switch st := s.(type) {
	case Closed:
		switch e := sig.(type) {
		case Failure:
			if st.Failures >= 2 {
				return Enter[StateA]{Open{e.Endpoint}}
			}
			return Stay[StateA]{Closed{st.Failures + 1}}
		case Retry:
			return Ignore{}
		}
	case Open:
		switch sig.(type) {
		case Retry:
			return Enter[StateA]{Closed{0}}
		case Failure:
			return Done[string]{"open"}
		}
	}
	panic("ergo: defect: no case of the match matched")
}

// applyA is tidewalk's Apply: Enter and Stay take Next, the others keep s.
//
//go:noinline
func applyA(s StateA, d DecisionA[StateA, string]) StateA {
	switch v := d.(type) {
	case Enter[StateA]:
		return v.Next
	case Stay[StateA]:
		return v.Next
	case Ignore, Reject, Done[string]:
		return s
	}
	panic("ergo: defect: no case of the match matched")
}

func textStateA(s StateA) string {
	switch v := s.(type) {
	case Closed:
		return "Closed{" + strconv.FormatInt(v.Failures, 10) + "}"
	case Open:
		return "Open{" + v.Endpoint + "}"
	}
	panic("no variant")
}

func textDecisionA(d DecisionA[StateA, string]) string {
	switch v := d.(type) {
	case Enter[StateA]:
		return "Enter{" + textStateA(v.Next) + "}"
	case Stay[StateA]:
		return "Stay{" + textStateA(v.Next) + "}"
	case Ignore:
		return "Ignore"
	case Reject:
		return "Reject"
	case Done[string]:
		return "Done{" + v.Output + "}"
	}
	panic("no variant")
}

// ---- Form D: flat typed slots ----

// StateD holds Closed.Failures in an int64 slot and Open.Endpoint in a
// string slot. Size 32 bytes on amd64.
type StateD struct {
	tag      uint8
	failures int64
	endpoint string
}

const (
	stateClosed uint8 = 1
	stateOpen   uint8 = 2
)

func ClosedD(c Closed) StateD { return StateD{tag: stateClosed, failures: c.Failures} }
func OpenD(o Open) StateD     { return StateD{tag: stateOpen, endpoint: o.Endpoint} }

// SignalD: Failure.Endpoint in a string slot; Retry has none.
type SignalD struct {
	tag      uint8
	endpoint string
}

const (
	signalFailure uint8 = 1
	signalRetry   uint8 = 2
)

func FailureD(f Failure) SignalD { return SignalD{tag: signalFailure, endpoint: f.Endpoint} }
func RetryD() SignalD            { return SignalD{tag: signalRetry} }

// DecisionD: Enter.Next and Stay.Next share the slot of type S; Done.Output
// has the slot of type O.
type DecisionD[S any, O any] struct {
	tag  uint8
	next S
	out  O
}

const (
	decEnter  uint8 = 1
	decStay   uint8 = 2
	decIgnore uint8 = 3
	decReject uint8 = 4
	decDone   uint8 = 5
)

//go:noinline
func stepD(s StateD, sig SignalD) DecisionD[StateD, string] {
	switch s.tag {
	case stateClosed:
		switch sig.tag {
		case signalFailure:
			if s.failures >= 2 {
				return DecisionD[StateD, string]{tag: decEnter, next: OpenD(Open{sig.endpoint})}
			}
			return DecisionD[StateD, string]{tag: decStay, next: ClosedD(Closed{s.failures + 1})}
		case signalRetry:
			return DecisionD[StateD, string]{tag: decIgnore}
		}
	case stateOpen:
		switch sig.tag {
		case signalRetry:
			return DecisionD[StateD, string]{tag: decEnter, next: ClosedD(Closed{0})}
		case signalFailure:
			return DecisionD[StateD, string]{tag: decDone, out: "open"}
		}
	}
	panic("ergo: defect: no case of the match matched")
}

// stepDx is stepD with one switch on the combined index of both tags.
//
//go:noinline
func stepDx(s StateD, sig SignalD) DecisionD[StateD, string] {
	switch uint(s.tag)*3 + uint(sig.tag) {
	case uint(stateClosed)*3 + uint(signalFailure):
		if s.failures >= 2 {
			return DecisionD[StateD, string]{tag: decEnter, next: OpenD(Open{sig.endpoint})}
		}
		return DecisionD[StateD, string]{tag: decStay, next: ClosedD(Closed{s.failures + 1})}
	case uint(stateClosed)*3 + uint(signalRetry):
		return DecisionD[StateD, string]{tag: decIgnore}
	case uint(stateOpen)*3 + uint(signalRetry):
		return DecisionD[StateD, string]{tag: decEnter, next: ClosedD(Closed{0})}
	case uint(stateOpen)*3 + uint(signalFailure):
		return DecisionD[StateD, string]{tag: decDone, out: "open"}
	}
	panic("ergo: defect: no case of the match matched")
}

//go:noinline
func applyD(s StateD, d DecisionD[StateD, string]) StateD {
	switch d.tag {
	case decEnter, decStay:
		return d.next
	case decIgnore, decReject, decDone:
		return s
	}
	panic("ergo: defect: no case of the match matched")
}

func (s StateD) wellFormed() bool {
	switch s.tag {
	case stateClosed:
		return s.endpoint == ""
	case stateOpen:
		return s.failures == 0
	}
	return false
}

func (d DecisionD[S, O]) wellFormedWith(next func(S) bool) bool {
	var zs S
	var zo O
	switch d.tag {
	case decEnter, decStay:
		return next(d.next) && any(d.out) == any(zo)
	case decIgnore, decReject:
		return any(d.next) == any(zs) && any(d.out) == any(zo)
	case decDone:
		return any(d.next) == any(zs)
	}
	return false
}

func textStateD(s StateD) string {
	switch s.tag {
	case stateClosed:
		return "Closed{" + strconv.FormatInt(s.failures, 10) + "}"
	case stateOpen:
		return "Open{" + s.endpoint + "}"
	}
	panic("no variant")
}

func textDecisionD(d DecisionD[StateD, string]) string {
	switch d.tag {
	case decEnter:
		return "Enter{" + textStateD(d.next) + "}"
	case decStay:
		return "Stay{" + textStateD(d.next) + "}"
	case decIgnore:
		return "Ignore"
	case decReject:
		return "Reject"
	case decDone:
		return "Done{" + d.out + "}"
	}
	panic("no variant")
}

// ---- Form E: tag + boxed payload ----

type StateE struct {
	tag uint8
	p   any
}

type SignalE struct {
	tag uint8
	p   any
}

type DecisionE struct {
	tag uint8
	p   any
}

//go:noinline
func stepE(s StateE, sig SignalE) DecisionE {
	switch s.tag {
	case stateClosed:
		st := s.p.(Closed)
		switch sig.tag {
		case signalFailure:
			e := sig.p.(Failure)
			if st.Failures >= 2 {
				return DecisionE{decEnter, Enter[StateE]{StateE{stateOpen, Open{e.Endpoint}}}}
			}
			return DecisionE{decStay, Stay[StateE]{StateE{stateClosed, Closed{st.Failures + 1}}}}
		case signalRetry:
			return DecisionE{decIgnore, Ignore{}}
		}
	case stateOpen:
		switch sig.tag {
		case signalRetry:
			return DecisionE{decEnter, Enter[StateE]{StateE{stateClosed, Closed{0}}}}
		case signalFailure:
			return DecisionE{decDone, Done[string]{"open"}}
		}
	}
	panic("ergo: defect: no case of the match matched")
}

//go:noinline
func applyE(s StateE, d DecisionE) StateE {
	switch d.tag {
	case decEnter:
		return d.p.(Enter[StateE]).Next
	case decStay:
		return d.p.(Stay[StateE]).Next
	case decIgnore, decReject, decDone:
		return s
	}
	panic("ergo: defect: no case of the match matched")
}

func textStateE(s StateE) string {
	switch s.tag {
	case stateClosed:
		return textStateA(s.p.(Closed))
	case stateOpen:
		return textStateA(s.p.(Open))
	}
	panic("no variant")
}

func textDecisionE(d DecisionE) string {
	switch d.tag {
	case decEnter:
		return "Enter{" + textStateE(d.p.(Enter[StateE]).Next) + "}"
	case decStay:
		return "Stay{" + textStateE(d.p.(Stay[StateE]).Next) + "}"
	case decIgnore:
		return "Ignore"
	case decReject:
		return "Reject"
	case decDone:
		return "Done{" + d.p.(Done[string]).Output + "}"
	}
	panic("no variant")
}

// ---- Form F: interface + tag method ----

type StateF interface{ stateTag() uint8 }
type SignalF interface{ signalTag() uint8 }
type DecisionF[S any, O any] interface{ decisionTag() uint8 }

func (Closed) stateTag() uint8      { return stateClosed }
func (Open) stateTag() uint8        { return stateOpen }
func (Failure) signalTag() uint8    { return signalFailure }
func (Retry) signalTag() uint8      { return signalRetry }
func (Enter[_]) decisionTag() uint8 { return decEnter }
func (Stay[_]) decisionTag() uint8  { return decStay }
func (Ignore) decisionTag() uint8   { return decIgnore }
func (Reject) decisionTag() uint8   { return decReject }
func (Done[_]) decisionTag() uint8  { return decDone }

//go:noinline
func stepF(s StateF, sig SignalF) DecisionF[StateF, string] {
	switch s.stateTag() {
	case stateClosed:
		st := s.(Closed)
		switch sig.signalTag() {
		case signalFailure:
			e := sig.(Failure)
			if st.Failures >= 2 {
				return Enter[StateF]{Open{e.Endpoint}}
			}
			return Stay[StateF]{Closed{st.Failures + 1}}
		case signalRetry:
			return Ignore{}
		}
	case stateOpen:
		switch sig.signalTag() {
		case signalRetry:
			return Enter[StateF]{Closed{0}}
		case signalFailure:
			return Done[string]{"open"}
		}
	}
	panic("ergo: defect: no case of the match matched")
}

//go:noinline
func applyF(s StateF, d DecisionF[StateF, string]) StateF {
	switch d.decisionTag() {
	case decEnter:
		return d.(Enter[StateF]).Next
	case decStay:
		return d.(Stay[StateF]).Next
	case decIgnore, decReject, decDone:
		return s
	}
	panic("ergo: defect: no case of the match matched")
}

func textDecisionF(d DecisionF[StateF, string]) string {
	switch v := d.(type) {
	case Enter[StateF]:
		return "Enter{" + textStateA(v.Next.(StateA)) + "}"
	case Stay[StateF]:
		return "Stay{" + textStateA(v.Next.(StateA)) + "}"
	case Ignore:
		return "Ignore"
	case Reject:
		return "Reject"
	case Done[string]:
		return "Done{" + v.Output + "}"
	}
	panic("no variant")
}

// ---- Inputs ----

// breakerSignals is a seeded signal sequence; each form converts it.
func breakerSignals(n int, seed uint64) []Failure {
	r := newRand(seed)
	out := make([]Failure, n)
	for i := range out {
		switch r.next() % 3 {
		case 0:
			out[i] = Failure{} // Retry: an empty endpoint marks it
		case 1:
			out[i] = Failure{Endpoint: "svc-a"}
		default:
			out[i] = Failure{Endpoint: "svc-b"}
		}
	}
	return out
}

func signalsA(raw []Failure) []SignalA {
	out := make([]SignalA, len(raw))
	for i, f := range raw {
		if f.Endpoint == "" {
			out[i] = Retry{}
		} else {
			out[i] = f
		}
	}
	return out
}

func signalsD(raw []Failure) []SignalD {
	out := make([]SignalD, len(raw))
	for i, f := range raw {
		if f.Endpoint == "" {
			out[i] = RetryD()
		} else {
			out[i] = FailureD(f)
		}
	}
	return out
}

func signalsE(raw []Failure) []SignalE {
	out := make([]SignalE, len(raw))
	for i, f := range raw {
		if f.Endpoint == "" {
			out[i] = SignalE{signalRetry, Retry{}}
		} else {
			out[i] = SignalE{signalFailure, f}
		}
	}
	return out
}

func signalsF(raw []Failure) []SignalF {
	out := make([]SignalF, len(raw))
	for i, f := range raw {
		if f.Endpoint == "" {
			out[i] = Retry{}
		} else {
			out[i] = f
		}
	}
	return out
}

// rng is splitmix64: seeded and the same on every host.
type rng struct{ s uint64 }

func newRand(seed uint64) *rng { return &rng{seed} }

func (r *rng) next() uint64 {
	r.s += 0x9e3779b97f4a7c15
	z := r.s
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return z ^ (z >> 31)
}
