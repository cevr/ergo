//go:build (linux || darwin) && (amd64 || arm64)

package ot

import (
	"bytes"
	"flag"
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"
)

var rawSeed = flag.Int64("raw.seed", -1, "replay one seed of TestRawCallsStayInTheirLengths")

// canary fills the tail of a buffer the library must not touch.
const canary = 0xa5

// sentinel is the record put after the records the library may write.
var sentinel = InputEvent{Kind: 0xee, Action: 0xee, Code: 0xdeadbeef, X: 0xbeef, TextLen: 0xdeadbeef}

// TestRawCallsStayInTheirLengths: the hand-written calls of raw.go pass
// the library each slice's own length, so it never writes past it. For
// seeded screens (1..60 by 1..20 cells, a line of seeded text), each call
// gets a slice cut from a longer buffer whose tail holds a canary:
//   - SessionReadOutput reads the frame's bytes with seeded capacities
//     (1..300); each ticket holds at most that many bytes;
//   - LeaseResolvedChars writes the cells into buffers one byte short of
//     the size the library states (BufferLeaseGetRealCharSize), of that
//     size and of a seeded size, with cell lengths of width*height cells;
//   - SessionInputFeed parses up to 24 seeded key and mouse events into
//     4..28 records (OT_INPUT_EVENTS_MIN is 4) and a payload of
//     OT_INPUT_PAYLOAD_BYTES_MIN..+64 bytes.
//
// After every call the canaries hold, whatever the call's status, and
// what the library reports written fits the length passed. A failing seed
// names its replay: go test -run TestRawCallsStayInTheirLengths -raw.seed=N.
//
// Mutants (testdata/mutants.json): OTUL-feed-records-len-plus-one,
// OTUL-read-output-len-plus-one, OTUL-resolved-chars-len-plus-one: a raw
// call passes len+1 for a slice (the library then writes one past it).
func TestRawCallsStayInTheirLengths(t *testing.T) {
	if err := Open(); err != nil {
		t.Fatal(err)
	}
	owner := NewOwner()
	defer func() {
		if err := owner.Close(); err != nil {
			t.Error(err)
		}
	}()
	seeds := []int64{}
	if *rawSeed >= 0 {
		seeds = append(seeds, *rawSeed)
	} else {
		for s := range int64(30) {
			seeds = append(seeds, s+1)
		}
	}
	var fatal any
	var failed []string
	err := owner.Do(func() {
		defer func() { fatal = recover() }()
		ctx, err := ContextCreate(ContextOptions{ObjectCapacity: 1024, RenderCellsMax: 512 * 512})
		must("ContextCreate", err)
		defer func() { must("ContextDestroy", ContextDestroy(ctx)) }()
		for _, seed := range seeds {
			for _, f := range rawSeedRun(ctx, seed) {
				failed = append(failed, fmt.Sprintf("seed %d: %s (replay: go test -run TestRawCallsStayInTheirLengths -raw.seed=%d)", seed, f, seed))
			}
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if fatal != nil {
		t.Fatal(fatal)
	}
	for _, f := range failed {
		t.Error(f)
	}
}

func must(what string, err error) {
	if err != nil {
		panic(what + ": " + err.Error())
	}
}

// guarded is a slice of n bytes cut from a buffer whose tail of 32 bytes
// holds the canary; intact says whether the tail still does.
func guarded(n int) (b []byte, intact func() bool) {
	all := make([]byte, n+32)
	for i := range all {
		all[i] = canary
	}
	return all[:n:n], func() bool { return bytes.Count(all[n:], []byte{canary}) == 32 }
}

func rawSeedRun(ctx *Context, seed int64) (failed []string) {
	r := rand.New(rand.NewPCG(uint64(seed), 0x0751))
	width, height := 1+r.IntN(60), 1+r.IntN(20)
	session, err := SessionCreate(ctx, SessionOptions{ChunkSize: 64 << 10, SpanCapacity: 64, MaxBytes: 4 << 20, ControlCapacity: 4096})
	must("SessionCreate", err)
	defer func() { must("SessionDestroy", SessionDestroy(ctx, session)) }()
	must("SessionAttachRenderer", SessionAttachRenderer(ctx, session, SessionRendererOptions{Width: uint32(width), Height: uint32(height), Remote: 1}))
	root, err := SceneCreateNode(ctx, session, OT_SCENE_ROOT, 1)
	must("SceneCreateNode root", err)
	label, err := SceneCreateNode(ctx, session, OT_SCENE_TEXT, 2)
	must("SceneCreateNode text", err)
	words := make([]byte, r.IntN(2*width*height+1))
	for i := range words {
		words[i] = byte('!' + r.IntN(94))
	}
	must("SceneSetTextOptions", SceneSetTextOptions(ctx, label, SceneTextOptions{Foreground: [4]uint16{255, 255, 255, 255}}))
	must("SceneSetText", SceneSetText(ctx, label, words))
	must("SceneMoveNode", SceneMoveNode(ctx, label, root, 0))
	req, err := ScenePaint(ctx, session, [4]uint16{0, 0, 128, 255}, 1, 0)
	must("ScenePaint", err)
	if _, err := SceneFrameCommit(ctx, session, req, 0); err != nil {
		panic("SceneFrameCommit: " + err.Error())
	}

	// SessionReadOutput: the frame's bytes, read with seeded capacities.
	for reads := 0; ; reads++ {
		if reads > 1<<16 {
			return append(failed, "the frame's output never ends")
		}
		c := 1 + r.IntN(300)
		buf, intact := guarded(c)
		ticket, err := SessionReadOutput(ctx, session, buf)
		if !intact() {
			failed = append(failed, fmt.Sprintf("SessionReadOutput wrote past a capacity of %d", c))
		}
		if err != nil {
			panic("SessionReadOutput: " + err.Error())
		}
		if int(ticket.ByteCount) > c {
			failed = append(failed, fmt.Sprintf("SessionReadOutput reports %d bytes in a capacity of %d", ticket.ByteCount, c))
		}
		if ticket.ByteCount == 0 {
			break
		}
		must("SessionCompleteOutput", SessionCompleteOutput(ctx, session, ticket, 1))
	}

	// LeaseResolvedChars: one byte short of the stated size, the size, and
	// a seeded size; with and without line breaks.
	snap, err := SessionAcquireBufferLease(ctx, session, 0)
	must("SessionAcquireBufferLease", err)
	defer func() { must("BufferLeaseRelease", BufferLeaseRelease(ctx, snap.Lease)) }()
	cells := width * height
	for _, breaks := range []bool{false, true} {
		var lb uint32
		if breaks {
			lb = 1
		}
		size, err := BufferLeaseGetRealCharSize(ctx, snap.Lease, lb)
		must("BufferLeaseGetRealCharSize", err)
		caps := []int{int(size), r.IntN(int(size) + 9)}
		if size > 0 {
			caps = append(caps, int(size)-1)
		}
		for _, c := range caps {
			b, intactB := guarded(c)
			lengths, intactL := guarded(cells)
			n, err := LeaseResolvedChars(ctx, snap.Lease, b, breaks, lengths)
			if !intactB() {
				failed = append(failed, fmt.Sprintf("LeaseResolvedChars (breaks %v) wrote past a capacity of %d (the stated size is %d)", breaks, c, size))
			}
			if !intactL() {
				failed = append(failed, fmt.Sprintf("LeaseResolvedChars wrote past %d cell lengths", cells))
			}
			if err == nil && int(n) > c {
				failed = append(failed, fmt.Sprintf("LeaseResolvedChars reports %d bytes in a capacity of %d", n, c))
			}
		}
	}

	// SessionInputFeed: seeded events into seeded record and payload
	// capacities.
	events := []string{"a", "Z", "\x1b[A", "\x01", "\x7f", "\x1b[<0;5;3M", "\x1b[<0;5;3m", "\x1b[<65;2;2M", "\r", "\t"}
	var keys []byte
	for range 1 + r.IntN(24) {
		keys = append(keys, events[r.IntN(len(events))]...)
	}
	rc := int(OT_INPUT_EVENTS_MIN) + r.IntN(25)
	all := make([]InputEvent, rc+4)
	for i := range all {
		all[i] = sentinel
	}
	pc := int(OT_INPUT_PAYLOAD_BYTES_MIN) + r.IntN(65)
	payload, intactP := guarded(pc)
	d, err := SessionInputFeed(ctx, session, keys, 1, all[:rc:rc], payload)
	if slices.ContainsFunc(all[rc:], func(e InputEvent) bool { return e != sentinel }) {
		failed = append(failed, fmt.Sprintf("SessionInputFeed wrote past %d records (%d bytes of input)", rc, len(keys)))
	}
	if !intactP() {
		failed = append(failed, fmt.Sprintf("SessionInputFeed wrote past a payload of %d bytes", pc))
	}
	if err != nil {
		panic("SessionInputFeed: " + err.Error())
	}
	if int(d.Count) > rc || int(d.PayloadLen) > pc || int(d.Consumed) > len(keys) {
		failed = append(failed, fmt.Sprintf("SessionInputFeed reports %d records, %d payload bytes, %d consumed; capacities %d, %d, %d", d.Count, d.PayloadLen, d.Consumed, rc, pc, len(keys)))
	}
	return failed
}
