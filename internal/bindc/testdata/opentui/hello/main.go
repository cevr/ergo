// Command hello is OpenTUI's own hello example in Go, over the generated
// opentui package (testdata/opentui/go). It follows
// packages/native/examples/hello/src/main.zig at OpenTUI v0.8.2 (b7d9797f)
// step by step: in an Owner, it creates a context, a session and a remote
// renderer of 12 x 3 cells, a root, a box with a border and a label with
// the text "Hello"; it paints, commits and drains one frame into
// hello.ansi, sets the text to "Ready" and writes ready.ansi; it begins the
// session close, and destroys the context. The output goes through
// SessionDrainOutput with a Go writer function, where main.zig copies it
// with ot_session_read_output.
//
// After the two frames of main.zig, it checks the measure callback on the
// real library: a measure function on two leaves runs for each leaf during
// a frame; a measure function that panics makes ScenePaint panic again with
// a *CallbackPanic after C returned, and the next frame paints, commits and
// drains. Then it times a typed call and a writer callback (for
// information only).
//
// Usage: hello <libopentui> <out dir>. It prints "ok: <check>" for each
// check and "cost: <name> <ns/op>" for each timing, and exits 1 at the
// first failed check. TestLiveOpenTUIHello builds it in a temporary module.
package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"time"

	"ergo.local/bind/opentui"
)

// Values of opentui.h that the Go package does not export.
const (
	sceneRoot = 0 // OT_SCENE_ROOT
	sceneBox  = 1 // OT_SCENE_BOX
	sceneText = 2 // OT_SCENE_TEXT

	renderPresented = 0 // OT_RENDER_PRESENTED
	renderPending   = 1 // OT_RENDER_PENDING
)

func fail(format string, args ...any) {
	fmt.Printf("FAIL: "+format+"\n", args...)
	os.Exit(1)
}

func ok(check string) { fmt.Println("ok: " + check) }

func must(what string, err error) {
	if err != nil {
		fail("%s: %v", what, err)
	}
}

func main() {
	if len(os.Args) != 3 {
		fail("usage: hello <libopentui> <out dir>")
	}
	lib, out := os.Args[1], os.Args[2]
	// Load checks the ABI version (2) before it registers a function.
	must("Load", opentui.Load(lib))
	abi, err := opentui.ContextAbiVersion()
	must("ContextAbiVersion", err)
	if abi != 2 {
		fail("ot_context_abi_version: %d, want 2", abi)
	}
	ok("Load passed the ABI check (2)")

	// ot_context is thread-affine: every call on it runs on the owner thread.
	owner := opentui.NewOwner()
	if err := owner.Do(func() { hello(out) }); err != nil {
		fail("Owner.Do: %v", err)
	}
	if n := opentui.LiveCallbacks(); n != 0 {
		fail("LiveCallbacks after ContextDestroy: %d, want 0", n)
	}
	ok("LiveCallbacks is 0 after ContextDestroy")
	must("Owner.Close", owner.Close())
	ok("Owner.Close returned nil")
}

// hello runs on the owner thread.
func hello(out string) {
	context, err := opentui.ContextCreate(opentui.ContextOptions{ObjectCapacity: 8, RenderCellsMax: 36})
	must("ContextCreate", err)
	// main.zig: chunk_size 4096, chunk_count 2, span_capacity 2.
	session, err := opentui.SessionCreate(context, opentui.SessionOptions{ChunkSize: 4096, SpanCapacity: 2, MaxBytes: 2 * 4096})
	must("SessionCreate", err)
	must("SessionAttachRenderer", opentui.SessionAttachRenderer(context, session, opentui.SessionRendererOptions{Width: 12, Height: 3, Remote: 1}))

	root, err := opentui.SceneCreateNode(context, session, sceneRoot, 1)
	must("SceneCreateNode root", err)
	box, err := opentui.SceneCreateNode(context, session, sceneBox, 2)
	must("SceneCreateNode box", err)
	label, err := opentui.SceneCreateNode(context, session, sceneText, 3)
	must("SceneCreateNode label", err)
	// main.zig sets .borderSides = 15 over the defaults of scene.Paint
	// (scene.zig); the C struct has no defaults, so they are written here.
	must("SceneSetPaint", opentui.SceneSetPaint(context, box, opentui.ScenePaintOptions{
		Opacity:            1,
		BorderSides:        15,
		ShouldFill:         1,
		BorderColor:        [4]uint16{255, 255, 255, 255},
		FocusedBorderColor: [4]uint16{0, 170, 255, 255},
	}))
	must("SceneSetText Hello", opentui.SceneSetText(context, label, []byte("Hello")))
	must("SceneMoveNode box", opentui.SceneMoveNode(context, box, root, 0))
	must("SceneMoveNode label", opentui.SceneMoveNode(context, label, box, 0))

	last := writeFrame(context, session, filepath.Join(out, "hello.ansi"))
	must("SceneSetText Ready", opentui.SceneSetText(context, label, []byte("Ready")))
	last = writeFrame(context, session, filepath.Join(out, "ready.ansi"))
	ok("two frames drained through the Go writer")

	measure(context, session, root, label, last, out)
	timing(context, session, label)

	must("SessionClose", opentui.SessionClose(context, session))
	must("ContextDestroy", opentui.ContextDestroy(context))
	ok("session close begun and context destroyed")
}

// frame paints and commits one frame, drains its output through a Go
// writer, and returns the output and the frame id.
func frame(context *opentui.Context, session opentui.Handle) ([]byte, uint64) {
	req, err := opentui.ScenePaint(context, session, [4]uint16{0, 0, 0, 255}, 0, 0)
	must("ScenePaint", err)
	return commit(context, session, req), req.FrameId
}

func commit(context *opentui.Context, session opentui.Handle, req opentui.SceneFrameRequest) []byte {
	st, err := opentui.SceneFrameCommit(context, session, req, 0)
	if err != nil {
		_ = opentui.SceneFrameCancel(context, session, req.FrameId)
		fail("SceneFrameCommit: %v", err)
	}
	if st != renderPresented && st != renderPending {
		fail("SceneFrameCommit: render status %d, want presented or pending", st)
	}
	var got []byte
	writer := func(b []byte) int64 {
		got = append(got, b...)
		return int64(len(b))
	}
	for {
		n, err := opentui.SessionDrainOutput(context, session, 512, writer)
		must("SessionDrainOutput", err)
		if n == 0 {
			return got
		}
	}
}

func writeFrame(context *opentui.Context, session opentui.Handle, path string) uint64 {
	b, id := frame(context, session)
	must("write "+path, os.WriteFile(path, b, 0o644))
	return id
}

// measure checks the measure callback during frames. It adds two leaf boxes
// under the root with a measure function each; last is the id of the last
// frame. The leaves measure 0 x 0, so the box keeps its 3 rows. The frame
// after the panic sets the label to "Again" and goes to again.ansi.
func measure(context *opentui.Context, session, root, label opentui.Handle, last uint64, out string) {
	var leaves [2]opentui.Handle
	var ran []int
	for i := range leaves {
		leaf, err := opentui.SceneCreateNode(context, session, sceneBox, uint32(4+i))
		must("SceneCreateNode leaf", err)
		must("SceneMoveNode leaf", opentui.SceneMoveNode(context, leaf, root, uint32(1+i)))
		leaves[i] = leaf
		must("SceneSetMeasure", opentui.SceneSetMeasure(context, leaf, func(w float32, wm uint32, h float32, hm uint32) [2]float32 {
			ran = append(ran, i)
			return [2]float32{0, 0}
		}))
	}
	_, last = frame(context, session)
	if !slices.Contains(ran, 0) || !slices.Contains(ran, 1) {
		fail("measure functions that ran during the frame: %v, want each leaf's", ran)
	}
	ok("a measure function runs with its leaf's key during a frame")

	// A replacement invalidates the cached measurement, so the next paint
	// calls it.
	boom := errors.New("boom in measure")
	must("SceneSetMeasure (panics)", opentui.SceneSetMeasure(context, leaves[0], func(float32, uint32, float32, uint32) [2]float32 {
		panic(boom)
	}))
	p := paintPanic(context, session)
	if p == nil || p.Typedef != "ot_scene_measure_callback" || !errors.Is(p, boom) {
		fail("ScenePaint with a panicking measure: %#v, want a *CallbackPanic of ot_scene_measure_callback with %v", p, boom)
	}
	// ScenePaint panicked after C kept its draft, so the Go caller has no
	// frame request. OpenTUI gives a frame the id after the last one
	// (scene.zig: last_frame_id + 1); a wrong id is OT_STALE_FRAME and
	// cancels nothing.
	last++
	must("SceneFrameCancel of the draft", opentui.SceneFrameCancel(context, session, last))
	ok("a panicking measure function makes ScenePaint panic with a *CallbackPanic after C returned")

	must("SceneSetMeasure (replaces)", opentui.SceneSetMeasure(context, leaves[0], func(float32, uint32, float32, uint32) [2]float32 {
		return [2]float32{0, 0}
	}))
	must("SceneSetText Again", opentui.SceneSetText(context, label, []byte("Again")))
	if id := writeFrame(context, session, filepath.Join(out, "again.ansi")); id != last+1 {
		fail("the frame after the cancel: id %d, want %d", id, last+1)
	}
	ok("the next frame paints, commits and drains (C state intact)")
}

// paintPanic calls ScenePaint and returns the *CallbackPanic it panics with.
func paintPanic(context *opentui.Context, session opentui.Handle) (p *opentui.CallbackPanic) {
	defer func() {
		v := recover()
		p, _ = v.(*opentui.CallbackPanic)
		if v != nil && p == nil {
			fail("ScenePaint panicked with %#v, want a *CallbackPanic", v)
		}
	}()
	req, err := opentui.ScenePaint(context, session, [4]uint16{0, 0, 0, 255}, 0, 0)
	fail("ScenePaint returned (%v, %v), want a panic", req.FrameId, err)
	return nil
}

// timing prints the ns/op of one typed call on the context, and of a
// SessionDrainOutput call that makes one writer callback (one typed call,
// the registry and one callback). For information only: the cost test is
// TestLiveCallbackCost on the fixture.
func timing(context *opentui.Context, session, label opentui.Handle) {
	const calls, frames = 20000, 2000
	start := time.Now()
	for range calls {
		if _, err := opentui.SceneHasMeasure(context, label); err != nil {
			fail("SceneHasMeasure: %v", err)
		}
	}
	fmt.Printf("cost: typed-call-SceneHasMeasure %.1f\n", float64(time.Since(start).Nanoseconds())/calls)

	var spent time.Duration
	var drains, writes int
	writer := func(b []byte) int64 {
		writes++
		return int64(len(b))
	}
	for i := range frames {
		text := []byte("Hello")
		if i%2 == 1 {
			text = []byte("Ready")
		}
		must("SceneSetText", opentui.SceneSetText(context, label, text))
		req, err := opentui.ScenePaint(context, session, [4]uint16{0, 0, 0, 255}, 0, 0)
		must("ScenePaint", err)
		if st, err := opentui.SceneFrameCommit(context, session, req, 0); err != nil || st != renderPresented && st != renderPending {
			fail("SceneFrameCommit: render status %d, %v", st, err)
		}
		for {
			start := time.Now()
			w, err := opentui.SessionDrainOutput(context, session, 512, writer)
			d := time.Since(start)
			must("SessionDrainOutput", err)
			if w == 0 {
				break
			}
			spent += d
			drains++
		}
	}
	if drains == 0 || writes != drains {
		fail("timing: %d drain calls with output made %d writer callbacks, want one each", drains, writes)
	}
	fmt.Printf("cost: drain-call-with-one-writer-callback %.1f\n", float64(spent.Nanoseconds())/float64(drains))
	runtime.KeepAlive(writer)
}
