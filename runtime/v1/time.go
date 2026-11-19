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
//ergo:wait none
func TimeHostNow() Reading {
	n := time.Now()
	return Reading{Sec: n.Unix(), Nsec: int64(n.Nanosecond()), Mono: int64(n.Sub(hostStart))}
}

// TimeHostMono is the Mono of a read of the host clock, read from the
// monotonic clock alone: Go's time.Since of a time with a monotonic
// reading reads no wall clock (std-speed-1; time.Host's Since).
//
//ergo:host
//ergo:wait none
func TimeHostMono() int64 { return int64(time.Since(hostStart)) }

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
//
//ergo:wait member
func NewTimeline(t *rt.Task, sec int64, nsec int64) *Timeline {
	start := time.Unix(sec, nsec).UTC()
	return &Timeline{v: rt.NewManual(t, start), start: start}
}

// NewVirtualTimeline is testing.VirtualClock's timeline: an auto clock
// (rt.NewAuto) that reads time.Unix(sec, nsec). t, the calling task, is the
// first member of its domain, so the time moves on its own when every task
// of the domain waits; Advance still moves it by hand.
//
//ergo:wait member
func NewVirtualTimeline(t *rt.Task, sec int64, nsec int64) *Timeline {
	start := time.Unix(sec, nsec).UTC()
	return &Timeline{v: rt.NewAuto(t, start), start: start}
}

// Read is the timeline's time; Mono is the virtual time since the start.
// It takes no lock and builds no time.Time: the clock's Unix nanoseconds,
// split by floor division.
//
//ergo:wait none
func (l *Timeline) Read() Reading {
	ns := int64(l.v.Now())
	sec, nsec := ns/1e9, ns%1e9
	if nsec < 0 {
		sec, nsec = sec-1, nsec+1e9
	}
	return Reading{Sec: sec, Nsec: nsec, Mono: ns - l.start.UnixNano()}
}

// Wait suspends the caller's task t until Advance moves the time to d past
// the time Wait began. A d of zero or less returns at once. It is an
// interruption point: a stop of t ends the wait with rt.Interrupted, and
// the stopped sleeper's timer is removed, so it never wakes.
//
//ergo:wait caller
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
//
//ergo:wait settle
func (l *Timeline) Advance(t *rt.Task, d time.Duration) {
	if d < 0 {
		panic("time.Manual: Advance by a negative duration")
	}
	if err := l.v.AdvanceSettled(t, rt.Span(d)); err != nil {
		if err == rt.AdvanceRunning {
			panic("time.Manual: Advance while " + err.Error() + "; one task advances a clock")
		}
		raise(err)
	}
}

// Sleepers is the number of deadlines armed on the timeline: the tasks in
// Wait and the alarms of its AlarmClock.
//
//ergo:wait none
func (l *Timeline) Sleepers() int { return l.v.Pending() }

// TimeAsleep is time.Asleep: whether the task of tk, or a task started
// below it, sleeps on l. A task that ended sleeps on nothing (FK4-g2).
//
//ergo:wait none
func TimeAsleep[T any](l *Timeline, tk *Task[T]) bool {
	if tk == nil {
		return false
	}
	return l.v.Asleep(tk.h.Task())
}

// Alarms is the timeline's AlarmClock (a Manual's or a VirtualClock's
// Scheduler.Alarms): an alarm's deadline is armed on the timeline like a
// sleep's, so Advance, or an idle domain of an auto clock, fires it.
//
//ergo:wait none
func (l *Timeline) Alarms() *AlarmClock {
	return NewAlarmClock(func() time.Duration { return time.Duration(l.v.Now()) },
		func(d time.Duration, f func()) Alarm { return l.v.AfterFunc(rt.Span(d), f) })
}

// AlarmClock is the prelude's AlarmClock, the result of Scheduler.Alarms:
// the program's Scheduler as the runtime's waits that run no Ergo code use
// it. Now reads the clock; After arms a function to run once a span
// passes, with no task and no sleep, and its Alarm cancels it. http.Serve
// keeps one deadline per connection and arms an alarm only when the
// deadline comes before the armed one (HTTP-FAST-b2). Only the runtime
// makes one (runtimeOnly): the host's, a timeline's, or a Go Sleeper's
// of a test (NewAlarmClock), so a provider written in Ergo gives the
// AlarmClock of the provider it sleeps on.
type AlarmClock struct {
	now    func() time.Duration
	arm    func(d time.Duration, f func()) Alarm
	report func(d time.Duration) // Reporting's, nil otherwise
}

// Alarm is one armed deadline of an AlarmClock. Stop disarms it and
// reports whether it was still armed: false once its function has
// started, which then runs to its end.
type Alarm interface {
	Stop() bool
}

// NewAlarmClock is the AlarmClock whose Now is now and whose After is
// arm. now's readings only compare and subtract: any fixed origin.
func NewAlarmClock(now func() time.Duration, arm func(d time.Duration, f func()) Alarm) *AlarmClock {
	return &AlarmClock{now: now, arm: arm}
}

// Reporting is c whose Began calls report: a test's clock learns that a
// bounded wait began and its alarm, if any, is armed, which an alarm
// alone no longer shows when the wait arms none.
func (c *AlarmClock) Reporting(report func(d time.Duration)) *AlarmClock {
	r := *c
	r.report = report
	return &r
}

// Now is the clock's time.
func (c *AlarmClock) Now() time.Duration { return c.now() }

// Deadline is the time d from now on the clock.
func (c *AlarmClock) Deadline(d time.Duration) time.Duration { return c.now() + d }

// Began tells a Reporting clock that a wait of span d began, once its
// alarm is armed; on any other clock it does nothing.
func (c *AlarmClock) Began(d time.Duration) {
	if c.report != nil {
		c.report(d)
	}
}

// After arms f to run on a goroutine of its own once d has passed on the
// clock. A d of zero or less starts it at once.
func (c *AlarmClock) After(d time.Duration, f func()) Alarm { return c.arm(d, f) }

// hostAlarms is the host's AlarmClock: the monotonic clock, and
// time.AfterFunc.
var hostAlarms = NewAlarmClock(func() time.Duration { return time.Since(hostStart) },
	func(d time.Duration, f func()) Alarm { return time.AfterFunc(d, f) })

// HostAlarms is LiveScheduler's Alarms: the host's timers, as
// time.AfterFunc arms them.
//
//ergo:host
//ergo:wait none
func HostAlarms() *AlarmClock { return hostAlarms }

// Alarmer is a Sleeper that gives its AlarmClock: every Scheduler table
// of a program, since Alarms is an operation of Scheduler. t is the
// calling task. A Go Sleeper of a test that has no Alarms keeps a
// runtime wait on its own path: a task that sleeps (http.Serve's phases).
type Alarmer interface {
	Alarms(t *rt.Task) *AlarmClock
}
