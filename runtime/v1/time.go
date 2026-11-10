package v1

import (
	"time"

	"ergo.local/prototype/runtime/rt"
)

// The host and manual-clock bodies of std time (docs/spec/stdlib.md, time;
// std-align S2b). The Ergo package keeps time.Time as a record of its own
// with unexported fields, so only package time builds one; these bodies
// give its parts as Reading, the one record they share with it. The pure
// bodies (Format, Parse, Sub) are in timetext.go.

// Reading is time.Reading: one read of a clock, as Unix seconds,
// nanoseconds in 0..999,999,999 and a monotonic reading in nanoseconds.
// The lowering binds the std record of this name to this Go type
// (docs/spec/stdlib.md, Types 4).
type Reading struct {
	Sec  int64
	Nsec int64
	Mono int64
}

// hostStart is the host clock's reading at the program's start. A host
// Reading's Mono is the monotonic time since it, so two readings compare by
// the monotonic clock, as Go's time.Time does.
var hostStart = time.Now()

// TimeHostNow is one read of the host clock (time.Host's Now): one
// time.Now, whose wall part gives Sec and Nsec and whose monotonic part
// gives Mono.
//
//ergo:host
func TimeHostNow() Reading {
	n := time.Now()
	return Reading{Sec: n.Unix(), Nsec: int64(n.Nanosecond()), Mono: int64(n.Sub(hostStart))}
}

// Timeline is the state of a time.Manual clock: a virtual time that moves
// only by Advance, and the sleepers waiting on it. Sleepers wake in
// deadline order, ties in the order they slept (rt.Virtual). It is a
// pointer, so every copy of a Manual shares one timeline.
type Timeline struct {
	v     *rt.Virtual
	start time.Time
}

// NewTimeline is time.Manual's timeline: a manual clock (rt.NewManual)
// that reads time.Unix(sec, nsec). t, the calling task, is the first
// member of its domain, so Advance waits for the tasks below it (fua-b).
func NewTimeline(t *rt.Task, sec int64, nsec int64) *Timeline {
	start := time.Unix(sec, nsec).UTC()
	return &Timeline{v: rt.NewManual(t, start), start: start}
}

// NewVirtualTimeline is testing.VirtualClock's timeline: an auto clock
// (rt.NewAuto) that reads time.Unix(sec, nsec). t, the calling task, is the
// first member of its domain, so the time moves on its own when every task
// of the domain waits; Advance still moves it by hand.
func NewVirtualTimeline(t *rt.Task, sec int64, nsec int64) *Timeline {
	start := time.Unix(sec, nsec).UTC()
	return &Timeline{v: rt.NewAuto(t, start), start: start}
}

// Read is the timeline's time; Mono is the virtual time since the start.
func (l *Timeline) Read() Reading {
	n := l.v.Now()
	return Reading{Sec: n.Unix(), Nsec: int64(n.Nanosecond()), Mono: int64(n.Sub(l.start))}
}

// Wait suspends the caller's task t until Advance moves the time to d past
// the time Wait began. A d of zero or less returns at once. It is an
// interruption point: a stop of t ends the wait with rt.Interrupted, and
// the stopped sleeper's timer is removed, so it never wakes.
func (l *Timeline) Wait(t *rt.Task, d time.Duration) {
	if err := rt.Sleep(t, l.v, d); err != nil {
		raise(err)
	}
}

// Advance is Advance(d) on the caller's task t: it moves the time forward
// by d, one deadline at a time, waking each sleeper whose deadline it
// reaches, in deadline order, and it returns once every task of the
// clock's domain but t waits again on a wait the clock sees
// (rt.AdvanceSettled): each timer due by then has fired and every task it
// woke has run to its next wait, an actor's step included (fua-b). A
// negative d is a defect: time never falls. One Advance of a clock runs at
// a time: a call while another task's Advance runs is a defect
// (rt.AdvanceRunning; batch-13 round 2, B1). A stop of t raises
// rt.Interrupted.
func (l *Timeline) Advance(t *rt.Task, d time.Duration) {
	if d < 0 {
		panic("time.Manual: Advance by a negative duration")
	}
	if err := l.v.AdvanceSettled(t, d); err != nil {
		if err == rt.AdvanceRunning {
			panic("time.Manual: Advance while " + err.Error() + "; one task advances a clock")
		}
		raise(err)
	}
}

// Sleepers is the number of sleepers waiting.
func (l *Timeline) Sleepers() int { return l.v.Pending() }
