// Command cffi1 drives the generated fixture package against real C
// libfixture.so to verify CFFI-1 invariants:
//
//   - oracle: seeded differential test across all 23 typed fixture functions
//     comparing the word-direct purego.SyscallN fast path against the
//     purego.RegisterFunc oracle (SetForceRegisterFunc(true)).
//   - stackgrowth: forced runtime.GC() and deep goroutine stack growth inside
//     re-entrant ContextWrite and NodeMeasure callbacks.
//   - pinbalance: 1,000 error-returning calls and 1,000 recovered CallbackPanic
//     sequences followed by forced GC + finalizer drain, proving zero leaked
//     runtime.Pinners.
//   - zeroalloc: testing.AllocsPerRun(1000, ...) asserting 0 allocs/op on
//     NodeCount, NodeSetName, NodeReadName (plus NodeSetBox, NodeGetBox, BoxZ).
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"runtime"
	"testing"
	"time"

	"ergo.local/bind/fixture"
)

func fail(format string, args ...any) {
	fmt.Printf("FAIL: "+format+"\n", args...)
	os.Exit(1)
}

func ok(msg string) { fmt.Println("ok: " + msg) }

func must(what string, err error) {
	if err != nil {
		fail("%s: %v", what, err)
	}
}

func errText(err error) string {
	if err == nil {
		return "<nil>"
	}
	return err.Error()
}

func catchPanic(fn func()) (caught any) {
	defer func() { caught = recover() }()
	fn()
	return nil
}

func main() {
	mode := flag.String("mode", "", "oracle|stackgrowth|pinbalance|zeroalloc")
	lib := flag.String("lib", "", "path to libfixture shared library")
	seed := flag.Uint64("seed", 0x517cc1b727220a95, "PRNG seed for oracle mode")
	flag.Parse()

	if *lib == "" {
		fail("missing -lib")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	must("Load", fixture.Load(*lib))

	switch *mode {
	case "oracle":
		runOracle(*seed)
	case "stackgrowth":
		runStackGrowth()
	case "pinbalance":
		runPinBalance()
	case "zeroalloc":
		runZeroAlloc()
	default:
		fail("unknown -mode %q", *mode)
	}
}

type oracleTrace struct {
	abi        uint32
	threadID   uint64
	pointLen   []float32
	nodes      int
	count      uint32
	boxes      []fixture.Box
	names      [][]byte
	visits     []fixture.Ticket
	writes     []string
	repeats    []int64
	measures   []fixture.Point
	measureAs  []fixture.Point
	measureAll []uint32
	counters   fixture.Counters
	shapes     []int64
	boxZs      []int64
	errs       []string
	panics     []string
}

func executeOracleSequence(seed uint64, forceReg bool) oracleTrace {
	fixture.SetForceRegisterFunc(forceReg)
	r := rand.New(rand.NewPCG(seed, seed^0xa5a5a5a55a5a5a5a))
	var tr oracleTrace

	v, err := fixture.AbiVersion()
	must("AbiVersion", err)
	tr.abi = v

	tid, err := fixture.ThreadId()
	must("ThreadId", err)
	tr.threadID = tid

	for range 8 {
		px := float32(r.IntN(200)-100) / 4.0
		py := float32(r.IntN(200)-100) / 4.0
		pl, err := fixture.PointLength(fixture.Point{X: px, Y: py})
		must("PointLength", err)
		tr.pointLen = append(tr.pointLen, pl)
	}

	// Error case on ContextCreate (invalid capacity 0).
	_, err = fixture.ContextCreate(fixture.ContextOptions{NodeCapacity: 0})
	tr.errs = append(tr.errs, errText(err))

	ctx, err := fixture.ContextCreate(fixture.ContextOptions{NodeCapacity: 16, Strict: r.IntN(2) == 1})
	must("ContextCreate", err)
	defer func() {
		must("ContextDestroy", fixture.ContextDestroy(ctx))
	}()

	var nodes []fixture.Node
	for i := range 6 {
		n, err := fixture.NodeCreate(ctx, uint32(i+1))
		must("NodeCreate", err)
		nodes = append(nodes, n)
	}
	tr.nodes = len(nodes)

	// Set/get boxes and names with seeded values.
	for i, n := range nodes {
		box := fixture.Box{
			Kind:    uint8(r.IntN(255)),
			Origin:  [2]float64{float64(r.IntN(100)-50) * 0.25, float64(r.IntN(100)-50) * 0.5},
			Corner:  fixture.Point{X: float32(i) + 0.5, Y: float32(i)*2 - 1.25},
			Color:   [4]uint16{uint16(r.Uint32()), uint16(r.Uint32()), uint16(r.Uint32()), uint16(r.Uint32())},
			Layer:   int8(r.IntN(256) - 128),
			Depth:   int16(r.IntN(65536) - 32768),
			Z:       int64(r.Uint64()),
			Visible: fixture.Flag(r.IntN(2) == 1),
			Age:     fixture.Seconds(float64(i) * 1.125),
		}
		for j := range box.Name {
			box.Name[j] = byte('a' + r.IntN(26))
		}
		must("NodeSetBox", fixture.NodeSetBox(ctx, n, box))
		got, err := fixture.NodeGetBox(ctx, n)
		must("NodeGetBox", err)
		tr.boxes = append(tr.boxes, got)

		nameLen := r.IntN(32)
		var name []byte
		if nameLen > 0 {
			name = make([]byte, nameLen)
			for j := range name {
				name[j] = byte('A' + r.IntN(26))
			}
		}
		must("NodeSetName", fixture.NodeSetName(ctx, n, name))
		readBuf := make([]byte, 64)
		gotN, err := fixture.NodeReadName(ctx, n, readBuf)
		must("NodeReadName", err)
		tr.names = append(tr.names, append([]byte(nil), readBuf[:gotN]...))
	}

	// ContextVisit callback.
	must("ContextVisit", fixture.ContextVisit(ctx, func(v fixture.Visit) {
		tr.visits = append(tr.visits, v.Ticket)
	}))

	// ContextWrite: full, partial backpressure (0), invalid (-1), and panic.
	payload := make([]byte, 37)
	for i := range payload {
		payload[i] = byte('0' + (i+int(seed))%10)
	}
	var gotWrite bytes.Buffer
	written, err := fixture.ContextWrite(ctx, payload, 7, func(b []byte) int64 {
		gotWrite.Write(b)
		return int64(len(b))
	})
	must("ContextWrite", err)
	tr.writes = append(tr.writes, fmt.Sprintf("%d:%s", written, gotWrite.String()))

	written, err = fixture.ContextWrite(ctx, payload, 5, func(b []byte) int64 {
		return 0
	})
	must("ContextWrite backpressure", err)
	tr.writes = append(tr.writes, fmt.Sprintf("bp:%d", written))

	_, err = fixture.ContextWrite(ctx, payload, 5, func(b []byte) int64 {
		return -1
	})
	tr.errs = append(tr.errs, errText(err))

	p := catchPanic(func() {
		_, _ = fixture.ContextWrite(ctx, payload, 5, func(b []byte) int64 {
			panic("write-boom")
		})
	})
	tr.panics = append(tr.panics, fmt.Sprintf("%T:%v", p, p))

	// ContextRepeat.
	rep, err := fixture.ContextRepeat(ctx, 11, func(b []byte) int64 {
		return int64(len(b))
	})
	must("ContextRepeat", err)
	tr.repeats = append(tr.repeats, rep)

	// NodeSetMeasure + NodeMeasure + NodeMeasureAs + ContextMeasureAll.
	for i := 0; i < 3; i++ {
		scale := float32(i + 2)
		must("NodeSetMeasure", fixture.NodeSetMeasure(ctx, nodes[i], func(w float32, wm uint32, h float32, hm uint32) [2]float32 {
			return [2]float32{w*scale + float32(wm), h*scale + float32(hm)}
		}))
		pt, err := fixture.NodeMeasure(ctx, nodes[i], 12.5+float32(i), 7.25+float32(i))
		must("NodeMeasure", err)
		tr.measures = append(tr.measures, pt)
	}
	// NodeMeasureAs using another node's key (unregistered key -> NaN / unwritten).
	ptAs, err := fixture.NodeMeasureAs(ctx, nodes[0], nodes[5])
	must("NodeMeasureAs", err)
	if math.IsNaN(float64(ptAs.X)) {
		ptAs.X = -999
	}
	if math.IsNaN(float64(ptAs.Y)) {
		ptAs.Y = -999
	}
	tr.measureAs = append(tr.measureAs, ptAs)

	// NodeMeasureAs using own key (registered).
	ptOwn, err := fixture.NodeMeasureAs(ctx, nodes[1], nodes[1])
	must("NodeMeasureAs own", err)
	tr.measureAs = append(tr.measureAs, ptOwn)

	mc, err := fixture.ContextMeasureAll(ctx, 3.25, 4.75)
	must("ContextMeasureAll", err)
	tr.measureAll = append(tr.measureAll, mc)

	// Clear measure callback on nodes[0].
	must("NodeSetMeasure clear", fixture.NodeSetMeasure(ctx, nodes[0], nil))
	_, err = fixture.NodeMeasure(ctx, nodes[0], 1, 2)
	tr.errs = append(tr.errs, errText(err))

	// ShapeArea across all record kinds + error cases.
	for _, rec := range []fixture.ShapeHeaderRecord{
		fixture.ShapeHeader{Kind: fixture.FX_SHAPE_NONE},
		fixture.ShapeDot{Header: fixture.ShapeHeader{Kind: fixture.FX_SHAPE_DOT}, Weight: -19},
		fixture.ShapeRect{Header: fixture.ShapeHeader{Kind: fixture.FX_SHAPE_RECT}, Width: 11, Height: 13},
	} {
		a, err := fixture.ShapeArea(ctx, rec)
		must("ShapeArea", err)
		tr.shapes = append(tr.shapes, a)
	}
	_, err = fixture.ShapeArea(ctx, nil)
	tr.errs = append(tr.errs, errText(err))
	_, err = fixture.ShapeArea(ctx, fixture.ShapeHeader{Kind: fixture.FX_SHAPE_RECT})
	tr.errs = append(tr.errs, errText(err))

	// BoxZ nil and non-nil.
	zNil, err := fixture.BoxZ(ctx, nil)
	must("BoxZ nil", err)
	tr.boxZs = append(tr.boxZs, zNil)
	zVal, err := fixture.BoxZ(ctx, &fixture.Box{Z: -424242})
	must("BoxZ val", err)
	tr.boxZs = append(tr.boxZs, zVal)

	// Destroy one node, verify NodeCount and stale error.
	must("NodeDestroy", fixture.NodeDestroy(ctx, nodes[5]))
	tr.errs = append(tr.errs, errText(fixture.NodeDestroy(ctx, nodes[5])))

	cnt, err := fixture.NodeCount(ctx)
	must("NodeCount", err)
	tr.count = cnt

	ctrs, err := fixture.ContextCounters(ctx)
	must("ContextCounters", err)
	tr.counters = ctrs

	return tr
}

func runOracle(baseSeed uint64) {
	// First check ContextAge and float functions on the exact SAME context
	// toggling forceRegisterFunc, so any float ABI mismatch is caught directly.
	ctx, err := fixture.ContextCreate(fixture.ContextOptions{NodeCapacity: 4})
	must("ContextCreate same-ctx", err)
	node, err := fixture.NodeCreate(ctx, 1)
	must("NodeCreate same-ctx", err)
	must("NodeSetMeasure same-ctx", fixture.NodeSetMeasure(ctx, node, func(w float32, _ uint32, h float32, _ uint32) [2]float32 {
		return [2]float32{w*1.5 + 0.25, h*2.5 - 0.75}
	}))

	fixture.SetForceRegisterFunc(false)
	ageFast, err := fixture.ContextAge(ctx)
	must("ContextAge fast", err)
	ptFast, err := fixture.NodeMeasure(ctx, node, 12.5, 7.25)
	must("NodeMeasure fast", err)
	allFast, err := fixture.ContextMeasureAll(ctx, 9.5, 4.25)
	must("ContextMeasureAll fast", err)

	fixture.SetForceRegisterFunc(true)
	ageReg, err := fixture.ContextAge(ctx)
	must("ContextAge reg", err)
	ptReg, err := fixture.NodeMeasure(ctx, node, 12.5, 7.25)
	must("NodeMeasure reg", err)
	allReg, err := fixture.ContextMeasureAll(ctx, 9.5, 4.25)
	must("ContextMeasureAll reg", err)
	fixture.SetForceRegisterFunc(false)

	if math.Float64bits(float64(ageFast)) != math.Float64bits(float64(ageReg)) {
		fail("ContextAge mismatch: fast=%v reg=%v", ageFast, ageReg)
	}
	if ptFast != ptReg {
		fail("NodeMeasure mismatch: fast=%+v reg=%+v", ptFast, ptReg)
	}
	if allFast != allReg {
		fail("ContextMeasureAll mismatch: fast=%d reg=%d", allFast, allReg)
	}
	must("ContextDestroy same-ctx", fixture.ContextDestroy(ctx))

	// Run 8 seeded full sequences comparing SyscallN fast path vs RegisterFunc.
	for i := range uint64(8) {
		seed := baseSeed + i*0x9e3779b97f4a7c15
		fast := executeOracleSequence(seed, false)
		reg := executeOracleSequence(seed, true)
		if fmt.Sprintf("%#v", fast) != fmt.Sprintf("%#v", reg) {
			fail("oracle seed %d mismatch:\nfast=%#v\nreg =%#v", seed, fast, reg)
		}
	}
	fixture.SetForceRegisterFunc(false)
	ok("oracle matched across all 23 typed functions")
}

// growStack forces deep goroutine stack growth (~128 KB) and GC so any
// unpinned stack-allocated Go pointer passed into C would be relocated/invalidated.
//
//go:noinline
func growStack(depth int, acc byte) byte {
	var frame [4096]byte
	frame[0] = acc ^ byte(depth)
	frame[len(frame)-1] = byte(depth)
	if depth == 0 {
		runtime.GC()
		return frame[0] ^ frame[len(frame)-1]
	}
	return growStack(depth-1, frame[0]+frame[len(frame)-1])
}

func runStackGrowth() {
	ctx, err := fixture.ContextCreate(fixture.ContextOptions{NodeCapacity: 8, Strict: true})
	must("ContextCreate", err)
	defer func() { must("ContextDestroy", fixture.ContextDestroy(ctx)) }()

	node, err := fixture.NodeCreate(ctx, 42)
	must("NodeCreate", err)

	// Stress 1: ContextWrite with pinned input buffer + pinned out_written
	// while callback forces GC + deep stack growth + re-entrant fixture calls.
	input := make([]byte, 4096)
	for i := range input {
		input[i] = byte((i*31 + 17) & 0xff)
	}
	expected := append([]byte(nil), input...)
	var collected []byte
	var sink byte
	calls := 0
	written, err := fixture.ContextWrite(ctx, input, 64, func(chunk []byte) int64 {
		calls++
		collected = append(collected, chunk...)
		// Force stack growth + GC on every 4th chunk, plus re-entrant C calls.
		if calls%4 == 0 {
			sink ^= growStack(32, byte(calls))
			name := []byte(fmt.Sprintf("reentrant-%d", calls))
			must("reentrant NodeSetName", fixture.NodeSetName(ctx, node, name))
			buf := make([]byte, 32)
			n, err := fixture.NodeReadName(ctx, node, buf)
			if err != nil || string(buf[:n]) != string(name) {
				fail("reentrant NodeReadName: %d %q %v", n, buf[:n], err)
			}
			c, err := fixture.NodeCount(ctx)
			if err != nil || c != 1 {
				fail("reentrant NodeCount: %d %v", c, err)
			}
		}
		return int64(len(chunk))
	})
	must("ContextWrite stackgrowth", err)
	if int(written) != len(expected) || !bytes.Equal(collected, expected) {
		fail("ContextWrite corrupted under stack growth: written=%d len=%d equal=%v", written, len(expected), bytes.Equal(collected, expected))
	}

	// Stress 2: NodeMeasure with pinned node & out_size while callback forces
	// GC + deep stack growth + re-entrant fixture calls.
	must("NodeSetMeasure stackgrowth", fixture.NodeSetMeasure(ctx, node, func(w float32, _ uint32, h float32, _ uint32) [2]float32 {
		sink ^= growStack(36, 7)
		c, err := fixture.NodeCount(ctx)
		if err != nil || c != 1 {
			fail("measure reentrant NodeCount: %d %v", c, err)
		}
		return [2]float32{w + 10.5, h + 20.25}
	}))
	for i := range 32 {
		w := float32(i) + 1.5
		h := float32(i)*2 + 0.75
		pt, err := fixture.NodeMeasure(ctx, node, w, h)
		must("NodeMeasure stackgrowth", err)
		if pt.X != w+10.5 || pt.Y != h+20.25 {
			fail("NodeMeasure corrupted under stack growth: got=%+v want={%v %v}", pt, w+10.5, h+20.25)
		}
	}
	_ = sink
	ok("stack growth and GC preserved all pinned buffers")
}

func forceFinalizers() {
	done := make(chan struct{})
	func() {
		canary := new(int)
		*canary = 1
		runtime.SetFinalizer(canary, func(*int) { close(done) })
	}()
	for range 5 {
		runtime.GC()
		select {
		case <-done:
			// One extra GC + pause to give any concurrently queued pinner finalizer
			// time to run and panic if any Pinner was leaked.
			runtime.GC()
			time.Sleep(15 * time.Millisecond)
			return
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func runPinBalance() {
	ctx, err := fixture.ContextCreate(fixture.ContextOptions{NodeCapacity: 8})
	must("ContextCreate", err)
	defer func() { must("ContextDestroy", fixture.ContextDestroy(ctx)) }()

	stale, err := fixture.NodeCreate(ctx, 1)
	must("NodeCreate", err)
	must("NodeDestroy", fixture.NodeDestroy(ctx, stale))

	live, err := fixture.NodeCreate(ctx, 2)
	must("NodeCreate live", err)
	must("NodeSetMeasure panic", fixture.NodeSetMeasure(ctx, live, func(w float32, _ uint32, h float32, _ uint32) [2]float32 {
		if w < 0 {
			panic("measure-panic")
		}
		return [2]float32{w, h}
	}))

	buf := make([]byte, 32)
	msg := []byte("pin-balance-payload")

	// 1,000 error-returning calls across multiple pinned argument shapes.
	for i := range 1000 {
		switch i % 5 {
		case 0:
			_, err := fixture.ShapeArea(ctx, nil)
			var st fixture.Status
			if !errors.As(err, &st) || st != fixture.StatusInvalidArgument {
				fail("ShapeArea(nil) err=%v", err)
			}
		case 1:
			err := fixture.NodeSetName(ctx, stale, msg)
			var st fixture.Status
			if !errors.As(err, &st) || st != fixture.StatusStaleHandle {
				fail("NodeSetName(stale) err=%v", err)
			}
		case 2:
			_, err := fixture.NodeReadName(ctx, stale, buf)
			var st fixture.Status
			if !errors.As(err, &st) || st != fixture.StatusStaleHandle {
				fail("NodeReadName(stale) err=%v", err)
			}
		case 3:
			_, err := fixture.ContextCreate(fixture.ContextOptions{NodeCapacity: 0})
			var st fixture.Status
			if !errors.As(err, &st) || st != fixture.StatusInvalidArgument {
				fail("ContextCreate(0) err=%v", err)
			}
		case 4:
			_, err := fixture.ContextWrite(ctx, msg, 0, func(b []byte) int64 { return int64(len(b)) })
			var st fixture.Status
			if !errors.As(err, &st) || st != fixture.StatusInvalidArgument {
				fail("ContextWrite(max=0) err=%v", err)
			}
		}
	}

	// 1,000 recovered CallbackPanic sequences (both word-direct and RegisterFunc).
	for i := range 1000 {
		if i%2 == 0 {
			p := catchPanic(func() {
				_, _ = fixture.ContextWrite(ctx, msg, 4, func(b []byte) int64 {
					panic("write-panic")
				})
			})
			if p == nil {
				fail("ContextWrite panic not raised at %d", i)
			}
		} else {
			p := catchPanic(func() {
				_, _ = fixture.NodeMeasure(ctx, live, -1, 1)
			})
			if p == nil {
				fail("NodeMeasure panic not raised at %d", i)
			}
		}
	}

	// Trigger GC + finalizer execution. Any leaked runtime.Pinner triggers
	// runtime.pinnerLeakPanic() and terminates this process with status 2.
	forceFinalizers()
	if n := fixture.LiveCallbacks(); n != 1 {
		fail("LiveCallbacks = %d, want 1 (the registered measure callback)", n)
	}
	ok("pin balance verified across 1000 errors and 1000 panics")
}

func runZeroAlloc() {
	ctx, err := fixture.ContextCreate(fixture.ContextOptions{NodeCapacity: 4})
	must("ContextCreate", err)
	defer func() { must("ContextDestroy", fixture.ContextDestroy(ctx)) }()

	node, err := fixture.NodeCreate(ctx, 1)
	must("NodeCreate", err)

	name := []byte("hello-zero-alloc")
	readBuf := make([]byte, 32)
	box := fixture.Box{Kind: 1, Z: 99}
	clip := fixture.Box{Z: 123}

	// Warm up pool and pinner cache.
	for range 16 {
		must("warm NodeCount", func() error { _, e := fixture.NodeCount(ctx); return e }())
		must("warm NodeSetName", fixture.NodeSetName(ctx, node, name))
		must("warm NodeReadName", func() error { _, e := fixture.NodeReadName(ctx, node, readBuf); return e }())
		must("warm NodeSetBox", fixture.NodeSetBox(ctx, node, box))
		must("warm NodeGetBox", func() error { _, e := fixture.NodeGetBox(ctx, node); return e }())
		must("warm BoxZ", func() error { _, e := fixture.BoxZ(ctx, &clip); return e }())
	}

	checks := []struct {
		name string
		fn   func()
	}{
		{"NodeCount", func() {
			n, err := fixture.NodeCount(ctx)
			if err != nil || n != 1 {
				fail("NodeCount: %d %v", n, err)
			}
		}},
		{"NodeSetName", func() {
			if err := fixture.NodeSetName(ctx, node, name); err != nil {
				fail("NodeSetName: %v", err)
			}
		}},
		{"NodeReadName", func() {
			n, err := fixture.NodeReadName(ctx, node, readBuf)
			if err != nil || int(n) != len(name) {
				fail("NodeReadName: %d %v", n, err)
			}
		}},
		{"NodeSetBox", func() {
			if err := fixture.NodeSetBox(ctx, node, box); err != nil {
				fail("NodeSetBox: %v", err)
			}
		}},
		{"NodeGetBox", func() {
			got, err := fixture.NodeGetBox(ctx, node)
			if err != nil || got.Z != 99 {
				fail("NodeGetBox: %+v %v", got, err)
			}
		}},
		{"BoxZ", func() {
			z, err := fixture.BoxZ(ctx, &clip)
			if err != nil || z != 123 {
				fail("BoxZ: %d %v", z, err)
			}
		}},
	}
	for _, c := range checks {
		allocs := testing.AllocsPerRun(1000, c.fn)
		fmt.Printf("allocs %s %.2f\n", c.name, allocs)
		if allocs != 0 {
			fail("%s allocated %.2f allocs/op, want 0", c.name, allocs)
		}
	}
	ok("zero allocs/op verified")
}
