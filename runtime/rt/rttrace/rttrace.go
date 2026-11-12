// Package rttrace records the trace events of an rt task tree. It holds the
// bounded in-memory Recorder, which tests read directly, and the JSON Lines
// writer that emitted main uses when the program is asked to record a run.
// A program that never records links none of it: package rt holds only the
// event type, the Tracer interface and the emission points.
//
// The recorder keeps arrival order (Record.Seq), which is not a happens-before
// order of the run. Two events are ordered only when the runtime orders them:
// events of one goroutine; a task's start before its other events, except
// that a cancel-request may precede the start of a task stopped before it
// runs (a root interrupted before Run, a child born under a stopped owner or
// stopped before its goroutine runs) and a child's deadline may precede its
// start; a child's body-exit before its owner's observation of it. A law
// checker reads the partial order (docs/research/lawful-runtime-contract.md,
// docs/research/traceability.md), never Seq alone. The runtime's occurrence
// ID (rt.Event.ID) is a linear extension of that order over the emission
// points: an event that happens before another has the smaller ID. The links
// between events (rt.Event.Link, and a body-exit's per-reason
// rt.Event.Origins) are recorded by the runtime, so a reader follows them
// instead of inferring causes.
//
// The recorder keeps each rt.Event whole, including the error and panic
// values it carries; DefaultCapacity bounds the number of slots, not the
// bytes those values retain. Only the writer drops values: it serializes
// failures and defects by type name.
package rttrace

import (
	"sync/atomic"
	"time"

	"ergo.local/prototype/runtime/rt"
)

// DefaultCapacity is the number of events a recorder keeps unless told
// otherwise. A traced example run records 10 to 20 events; see
// docs/research/traceability.md, Measured overhead.
const DefaultCapacity = 4096

// Recorder is a bounded, non-blocking rt.Tracer. It keeps the first Cap
// events of a run and counts every later one as dropped, so the tail of a run
// longer than the bound is missing and says so. Recording an event never
// waits: it reserves a slot with one atomic add and writes it.
type Recorder struct {
	since   func() rt.Span // the time since New, on the monotonic clock
	slots   []slot
	next    atomic.Uint64
	dropped atomic.Uint64
}

type slot struct {
	done atomic.Bool // set after ev and at are written
	at   rt.Span
	ev   rt.Event
}

// Record is one recorded event with its arrival number and the time since the
// recorder started (monotonic clock). Under a virtual clock the time is the
// host's, not the program's. Recorder names no type of package time (binsize
// BS3): it is a Tracer, so the types it reaches are used in an interface,
// and a time.Time among them kept time.Time.Format in every program.
type Record struct {
	Seq  uint64
	Time rt.Span
	rt.Event
}

// New returns a recorder that keeps at most capacity events. A capacity below
// one keeps none and counts everything as dropped.
func New(capacity int) *Recorder {
	if capacity < 0 {
		capacity = 0
	}
	start := time.Now()
	return &Recorder{since: func() rt.Span { return rt.Span(time.Since(start)) }, slots: make([]slot, capacity)}
}

// Event records e. It is safe from any goroutine and never blocks.
func (r *Recorder) Event(e rt.Event) {
	i := r.next.Add(1) - 1
	if i >= uint64(len(r.slots)) {
		r.dropped.Add(1)
		return
	}
	s := &r.slots[i]
	s.at = r.since()
	s.ev = e
	s.done.Store(true)
}

// Cap is the number of events r keeps.
func (r *Recorder) Cap() int { return len(r.slots) }

// Dropped is the number of events r did not keep because it was full.
func (r *Recorder) Dropped() uint64 { return r.dropped.Load() }

// Records returns the kept events in arrival order. An event whose write is
// still in progress is left out and counted in inFlight; read after the
// traced tree has finished to get every kept event.
func (r *Recorder) Records() (recs []Record, inFlight int) {
	n := r.next.Load()
	if n > uint64(len(r.slots)) {
		n = uint64(len(r.slots))
	}
	recs = make([]Record, 0, n)
	for i := uint64(0); i < n; i++ {
		s := &r.slots[i]
		if !s.done.Load() {
			inFlight++
			continue
		}
		recs = append(recs, Record{Seq: i, Time: s.at, Event: s.ev})
	}
	return recs, inFlight
}

// Reset forgets every kept and dropped event. Call it only while no task of
// the traced tree runs.
func (r *Recorder) Reset() {
	n := r.next.Load()
	if n > uint64(len(r.slots)) {
		n = uint64(len(r.slots))
	}
	for i := uint64(0); i < n; i++ {
		r.slots[i].done.Store(false)
		r.slots[i].ev = rt.Event{}
	}
	r.next.Store(0)
	r.dropped.Store(0)
}
