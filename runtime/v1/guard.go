package v1

import (
	"sync"

	"ergo.local/prototype/runtime/rt"
)

// The test build's form of a view (LAWFFI LF2b; docs/spec/interop.md,
// Foreign facts 8). Where the run build passes BytesView(s) to a Go
// function, the test build writes
//
//	g := v1.GuardBytes(s, fn, param, at)
//	r := f(g.Bytes())
//	g.Done()
//
// GuardBytes copies s into fresh pages, so that the bytes end at the last
// byte of the last page, makes those pages read-only and leaves the next
// page unmapped (guard_unix.go, on linux and darwin). A write by the callee, a write it
// restores before it returns, and a read one byte past the end each fault
// at the access. Done makes the pages unreadable and keeps them in a ring
// of at most guardRingLen copies and guardRingBytes bytes, so a late read
// through a slice the callee kept faults too, until the copy leaves the
// ring. The fault is a ViewDefect that names the function, the parameter,
// the call and what the callee did (rt.FaultValue). Where pages cannot be
// mapped, the copy is on the heap: Done compares it with s, a write is the
// same defect, and Done poisons it, so a late read reads poison.

// ViewDefect is the defect of a Go call that broke the `reads` fact its
// view rests on: Verb is "wrote to", "read past the end of" or "kept".
type ViewDefect struct {
	Func, Param, At, Verb string
	// Cause is the runtime's fault, nil for the heap form's compare.
	Cause error
}

// String is the defect's text, which the entry's report writes after
// "panic: ".
func (d *ViewDefect) String() string {
	return d.Func + " " + d.Verb + " its argument " + d.Param + ", which its row vouches it only reads (" + d.At + "); remove the row"
}

// Error is String: a fault's value is an error.
func (d *ViewDefect) Error() string { return d.String() }

// BytesGuard is one guarded copy of a view's bytes.
type BytesGuard struct {
	s             string
	fn, param, at string
	data          []byte  // what the callee is given
	mem           []byte  // the mapping: the pages, then the unmapped page; nil for the heap form
	id            uint64  // the rt registration of mem
	pages         uintptr // the bytes of mem before the unmapped page
	done          bool
}

// guardRingLen and guardRingBytes bound the ring of finished copies.
const (
	guardRingLen   = 64
	guardRingBytes = 64 << 20
)

var (
	guardMu    sync.Mutex
	guardRing  []*BytesGuard
	guardBytes int
	// guardsMapped counts the copies ever mapped; guardsFreed those
	// unmapped (a test reads both: the ring's bound is a count).
	guardsMapped, guardsFreed int
)

// GuardBytes is the guarded copy of s for the Go call at at, whose
// function fn's parameter param the view would be.
func GuardBytes(s, fn, param, at string) *BytesGuard {
	g := &BytesGuard{s: s, fn: fn, param: param, at: at}
	if len(s) == 0 {
		return g
	}
	if !g.mapPages() {
		g.data = []byte(s)
	}
	return g
}

// Bytes is the slice the callee is given: nil for the empty string, as
// BytesView gives, with capacity equal to length.
func (g *BytesGuard) Bytes() []byte { return g.data }

// Done ends the call: the pages become unreadable and join the ring, or
// the heap form compares and poisons.
func (g *BytesGuard) Done() {
	if g.data == nil || g.done {
		return
	}
	if g.mem == nil {
		wrote := string(g.data) != g.s
		for i := range g.data {
			g.data[i] = 0xA5
		}
		g.done = true
		if wrote {
			panic(&ViewDefect{Func: g.fn, Param: g.param, At: g.at, Verb: "wrote to"})
		}
		return
	}
	g.seal()
	guardMu.Lock()
	g.done = true
	guardRing = append(guardRing, g)
	guardBytes += len(g.mem)
	var out []*BytesGuard
	for len(guardRing) > guardRingLen || guardBytes > guardRingBytes {
		old := guardRing[0]
		guardRing = guardRing[1:]
		guardBytes -= len(old.mem)
		out = append(out, old)
	}
	guardMu.Unlock()
	for _, old := range out {
		old.free()
	}
}

// fault is the value of a fault off bytes into g's mapping.
func (g *BytesGuard) fault(off uintptr, cause error) any {
	guardMu.Lock()
	done := g.done
	guardMu.Unlock()
	verb := "wrote to"
	switch {
	case done:
		verb = "kept"
	case off >= g.pages:
		verb = "read past the end of"
	}
	return &ViewDefect{Func: g.fn, Param: g.param, At: g.at, Verb: verb, Cause: cause}
}

// register records g's mapping with rt, so a fault in it is g's defect.
func (g *BytesGuard) register(base uintptr) {
	g.id = rt.GuardRegister(base, uintptr(len(g.mem)), g.fault)
	guardMu.Lock()
	guardsMapped++
	guardMu.Unlock()
}

// free forgets and unmaps a copy that left the ring.
func (g *BytesGuard) free() {
	rt.MapUnregister(g.id)
	g.unmap()
	guardMu.Lock()
	guardsFreed++
	guardMu.Unlock()
}

// guardsLive is the number of guarded copies mapped and not yet freed:
// at most guardRingLen once every call's Done has run.
func guardsLive() int {
	guardMu.Lock()
	defer guardMu.Unlock()
	return guardsMapped - guardsFreed
}
