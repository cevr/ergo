//go:build (linux || darwin) && (amd64 || arm64)

package ot

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"testing"

	"ergo.local/prototype/hosts/opentui/ot/internal/otgen"
)

// TestFilesCurrent: opentui.go is what bindc writes from the pinned header
// and the contract (go generate). Mutants: any hand edit of the binding
// (a struct_size autofill dropped, Load's ABI check loosened).
func TestFilesCurrent(t *testing.T) {
	want, err := otgen.Source(".")
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile("opentui.go")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("opentui.go is not what bindc writes from opentui.h and opentui.bind.json; run go generate in hosts/opentui/ot")
	}
}

// TestLoads drives the fetched library with no terminal: a context, a
// session and a remote renderer of 40 by 4 cells; a fill and a text drawn
// into a buffer; a frame rendered and its bytes read; the row read back
// from the cell planes; six input events parsed from terminal bytes. It
// needs the library (cmd/fetch) and fails naming the fetch command when
// it is absent. Mutant: a record's struct_size autofill skipped (OpenTUI
// refuses the record with OT_INVALID_ARGUMENT).
func TestLoads(t *testing.T) {
	if err := Open(); err != nil {
		t.Fatal(err)
	}
	if abi, err := ContextAbiVersion(); err != nil || abi != 2 {
		t.Fatalf("ContextAbiVersion: %d, %v; want 2", abi, err)
	}
	owner := NewOwner()
	defer func() {
		if err := owner.Close(); err != nil {
			t.Error(err)
		}
	}()
	// On the owner thread a failure panics (Do carries it out; t.Fatal's
	// Goexit would end the owner's runner).
	must := func(what string, err error) {
		if err != nil {
			panic(what + ": " + err.Error())
		}
	}
	var fatal any
	err := owner.Do(func() {
		defer func() { fatal = recover() }()
		ctx, err := ContextCreate(ContextOptions{ObjectCapacity: 1024, RenderCellsMax: 512 * 512})
		must("ContextCreate", err)
		defer func() { must("ContextDestroy", ContextDestroy(ctx)) }()
		session, err := SessionCreate(ctx, SessionOptions{ChunkSize: 64 << 10, SpanCapacity: 64, MaxBytes: 4 << 20, ControlCapacity: 4096})
		must("SessionCreate", err)
		defer func() { must("SessionDestroy", SessionDestroy(ctx, session)) }()
		const width, height = 40, 4
		must("SessionAttachRenderer", SessionAttachRenderer(ctx, session, SessionRendererOptions{Width: width, Height: height, Remote: 1}))
		// A scene of a padded root holding one text node, painted on a
		// blue background.
		const words = "Hello, Ergo \u2713 \u65e5\u672c"
		root, err := SceneCreateNode(ctx, session, OT_SCENE_ROOT, 1)
		must("SceneCreateNode root", err)
		label, err := SceneCreateNode(ctx, session, OT_SCENE_TEXT, 2)
		must("SceneCreateNode text", err)
		must("SceneSetTextOptions", SceneSetTextOptions(ctx, label, SceneTextOptions{Foreground: [4]uint16{255, 255, 255, 255}}))
		must("SceneSetText", SceneSetText(ctx, label, []byte(words)))
		must("SceneMoveNode", SceneMoveNode(ctx, label, root, 0))
		must("SceneSetStyle left", SceneSetStyle(ctx, root, OT_STYLE_VALUE, OT_STYLE_VALUE_PADDING, OT_EDGE_LEFT, OT_UNIT_POINT, 2, 0))
		must("SceneSetStyle top", SceneSetStyle(ctx, root, OT_STYLE_VALUE, OT_STYLE_VALUE_PADDING, OT_EDGE_TOP, OT_UNIT_POINT, 1, 0))
		req, err := ScenePaint(ctx, session, [4]uint16{0, 0, 128, 255}, 1, 0)
		must("ScenePaint", err)
		st, err := SceneFrameCommit(ctx, session, req, 0)
		must("SceneFrameCommit", err)
		if st != OT_RENDER_PRESENTED && st != OT_RENDER_PENDING {
			panic(fmt.Sprintf("SceneFrameCommit: render status %d", st))
		}

		var out []byte
		chunk := make([]byte, 4096)
		for {
			ticket, err := SessionReadOutput(ctx, session, chunk)
			must("SessionReadOutput", err)
			if ticket.ByteCount == 0 {
				break
			}
			out = append(out, chunk[:ticket.ByteCount]...)
			must("SessionCompleteOutput", SessionCompleteOutput(ctx, session, ticket, 1))
		}
		if !bytes.Contains(out, []byte(words)) {
			t.Errorf("the frame's bytes do not hold %q: %q", words, out)
		}

		snap, err := SessionAcquireBufferLease(ctx, session, 0)
		must("SessionAcquireBufferLease", err)
		cells := make([]byte, 4*width*height+height)
		lengths := make([]byte, width*height)
		n, err := LeaseResolvedChars(ctx, snap.Lease, cells, true, lengths)
		must("LeaseResolvedChars", err)
		must("BufferLeaseRelease", BufferLeaseRelease(ctx, snap.Lease))
		rows := strings.Split(strings.TrimSuffix(string(cells[:n]), "\n"), "\n")
		if len(rows) != height || strings.TrimRight(rows[1], " ") != "  "+words {
			t.Errorf("the cells read back: %q; want row 1 %q", rows, "  "+words)
		}

		keys := []byte("\x1b[Aa\x01\x7f\x1b[<0;5;3M\x1b[<65;5;3M")
		records := make([]InputEvent, 32)
		payload := make([]byte, 8192)
		d, err := SessionInputFeed(ctx, session, keys, 1, records, payload)
		must("SessionInputFeed", err)
		type ev struct {
			kind, action, mods uint8
			code               uint32
			x, y               uint16
		}
		want := []ev{
			{1, 1, 0, 57352, 0, 0}, // ArrowUp
			{1, 1, 0, 'a', 0, 0},
			{1, 1, 4, 'a', 0, 0}, // Ctrl+A
			{1, 1, 0, 127, 0, 0}, // Backspace
			{2, 1, 0, 0, 4, 2},   // mouse down at cell (4, 2)
			{2, 5, 0, 1, 4, 2},   // wheel
		}
		var got []ev
		for _, e := range records[:d.Count] {
			got = append(got, ev{e.Kind, e.Action, e.Modifiers, e.Code, e.X, e.Y})
		}
		if int(d.Consumed) != len(keys) || len(got) != len(want) {
			t.Errorf("input: consumed %d of %d, events %v; want %v", d.Consumed, len(keys), got, want)
			return
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("input event %d: %v, want %v", i, got[i], want[i])
			}
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if fatal != nil {
		t.Fatal(fatal)
	}
}
