// Command callbacks drives the callbacks of the fixture library through the
// generated typed layer, for TestLiveCallbacks and
// TestLiveCallbackRegistryModel. Modes:
//
//	callbacks <lib>  the callback checks: one "ok: <check>" line each;
//	exhaust <lib>    uses up purego's callback slots, then calls Load;
//	model <lib>      runs the commands of stdin (one per line) and prints
//	                 one observation line per command.
//
// It exits 1 at the first failed check.
package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"math"
	"os"
	"runtime"
	"strconv"
	"strings"

	"ergo.local/bind/fixture"
	"github.com/ebitengine/purego"
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

// wantStatus fails unless err is the Status want.
func wantStatus(what string, err error, want fixture.Status) {
	var st fixture.Status
	if !errors.As(err, &st) || st != want {
		fail("%s: error %v, want %v", what, err, want)
	}
}

// maxSlots is purego v0.11.3's callback slots per process (maxCB).
const maxSlots = 2000

// slotsLeft makes purego callbacks until purego panics, and returns how
// many it made.
func slotsLeft() (n int) {
	defer func() { _ = recover() }()
	for {
		purego.NewCallback(func() {})
		n++
	}
}

// rtldNoload is RTLD_NOLOAD: dlopen then opens nothing and fails unless
// the library is loaded already.
func rtldNoload() int {
	if runtime.GOOS == "darwin" {
		return 0x10
	}
	return 0x4
}

// loaded reports whether the library at path is loaded in this process.
func loaded(path string) bool {
	h, err := purego.Dlopen(path, purego.RTLD_NOW|rtldNoload())
	if err != nil || h == 0 {
		return false
	}
	_ = purego.Dlclose(h)
	return true
}

func main() {
	// A context belongs to the OS thread that creates it.
	runtime.LockOSThread()
	if len(os.Args) != 3 {
		fail("usage: callbacks callbacks|exhaust|model <lib>")
	}
	switch lib := os.Args[2]; os.Args[1] {
	case "callbacks":
		callbacks(lib)
	case "exhaust":
		exhaust(lib)
	case "model":
		model(lib)
	default:
		fail("unknown mode %s", os.Args[1])
	}
}

// boundTypedefs is the number of bound callback typedefs of the fixture.
const boundTypedefs = 3

func callbacks(lib string) {
	if loaded(lib) {
		fail("the library is loaded before Load")
	}
	must("Load", fixture.Load(lib))
	if !loaded(lib) {
		fail("the RTLD_NOLOAD probe does not see the loaded library")
	}
	ok("noload probe sees a loaded library")
	ctx, err := fixture.ContextCreate(fixture.ContextOptions{NodeCapacity: 8})
	must("ContextCreate", err)
	live0 := fixture.LiveCallbacks()
	if live0 != 0 {
		fail("LiveCallbacks after Load: %d, want 0", live0)
	}

	// A call-scoped writer: a borrowed buffer, an int64_t result.
	text := []byte("hello, callbacks")
	var got []byte
	n, err := fixture.ContextWrite(ctx, text, 5, func(data []byte) int64 {
		if l := fixture.LiveCallbacks(); l != 1 {
			fail("LiveCallbacks during the call: %d, want 1", l)
		}
		if len(data) > 5 {
			fail("a span of %d bytes, max 5", len(data))
		}
		got = append(got, data...)
		return int64(len(data))
	})
	must("ContextWrite", err)
	if int(n) != len(text) || !bytes.Equal(got, text) || fixture.LiveCallbacks() != 0 {
		fail("ContextWrite: %d %q live %d, want %d %q live 0", n, got, fixture.LiveCallbacks(), len(text), text)
	}
	ok("call-scoped borrowed buffer round trip")
	// The result reaches C: 0 stops the write (backpressure), -1 fails it.
	calls := 0
	n, err = fixture.ContextWrite(ctx, text, 4, func(data []byte) int64 {
		calls++
		if calls == 2 {
			return 0
		}
		return int64(len(data))
	})
	if err != nil || n != 4 || calls != 2 {
		fail("ContextWrite with backpressure: %d %v after %d calls, want 4 after 2", n, err, calls)
	}
	_, err = fixture.ContextWrite(ctx, text, 4, func(data []byte) int64 { return -1 })
	wantStatus("ContextWrite with a failing writer", err, fixture.StatusInvalidArgument)
	taken, err := fixture.ContextRepeat(ctx, 3, func(data []byte) int64 {
		if string(data) != "x" {
			fail("ContextRepeat data %q", data)
		}
		return 7
	})
	if err != nil || taken != 21 {
		fail("ContextRepeat: %d %v, want 21", taken, err)
	}
	ok("int64 result reaches C")

	// A const fx_visit * as a value.
	a, err := fixture.NodeCreate(ctx, 1)
	must("NodeCreate a", err)
	b, err := fixture.NodeCreate(ctx, 2)
	must("NodeCreate b", err)
	var visits []fixture.Visit
	must("ContextVisit", fixture.ContextVisit(ctx, func(v fixture.Visit) { visits = append(visits, v) }))
	want := []fixture.Visit{{Node: a, Ticket: fixture.Ticket(a.Slot)}, {Node: b, Ticket: fixture.Ticket(b.Slot)}}
	if len(visits) != 2 || visits[0] != want[0] || visits[1] != want[1] {
		fail("ContextVisit: %+v, want %+v", visits, want)
	}
	ok("value copy")

	// Registered callbacks keyed by their node.
	var log []string
	measure := func(name string, w0, h0 float32) fixture.MeasureCallback {
		return func(width float32, widthMode uint32, height float32, heightMode uint32) [2]float32 {
			if widthMode != 1 || heightMode != 1 {
				fail("%s: modes %d %d, want 1 1", name, widthMode, heightMode)
			}
			log = append(log, name)
			return [2]float32{width + w0, height + h0}
		}
	}
	must("NodeSetMeasure a", fixture.NodeSetMeasure(ctx, a, measure("a", 1, 2)))
	if strings.Join(log, " ") != "a" {
		fail("the set probe of a ran %v, want [a]", log)
	}
	must("NodeSetMeasure b", fixture.NodeSetMeasure(ctx, b, measure("b", 10, 20)))
	if fixture.LiveCallbacks() != 2 {
		fail("LiveCallbacks after two sets: %d, want 2", fixture.LiveCallbacks())
	}
	log = nil
	cnt, err := fixture.ContextMeasureAll(ctx, 3, 4)
	if err != nil || cnt != 2 || strings.Join(log, " ") != "a b" {
		fail("ContextMeasureAll: %d %v ran %v, want 2 [a b]", cnt, err, log)
	}
	p, err := fixture.NodeMeasure(ctx, b, 3, 4)
	if err != nil || p != (fixture.Point{X: 13, Y: 24}) {
		fail("NodeMeasure b: %+v %v, want {13 24}", p, err)
	}
	ok("registered callback keyed by its node; its out array reaches C")

	// A replacement: the set probe runs the new func, which takes effect.
	log = nil
	must("NodeSetMeasure a2", fixture.NodeSetMeasure(ctx, a, measure("a2", 100, 200)))
	if strings.Join(log, " ") != "a2" {
		fail("the set probe of the replacement ran %v, want [a2]", log)
	}
	if p, err := fixture.NodeMeasure(ctx, a, 1, 1); err != nil || p != (fixture.Point{X: 101, Y: 201}) {
		fail("NodeMeasure a after the replacement: %+v %v, want {101 201}", p, err)
	}
	if fixture.LiveCallbacks() != 2 {
		fail("LiveCallbacks after a replacement: %d, want 2", fixture.LiveCallbacks())
	}
	ok("replacement takes effect")

	// A clear: the clear probe runs the old func; then C has none.
	log = nil
	must("NodeSetMeasure a nil", fixture.NodeSetMeasure(ctx, a, nil))
	if strings.Join(log, " ") != "a2" {
		fail("the clear probe ran %v, want [a2]", log)
	}
	if fixture.LiveCallbacks() != 1 {
		fail("LiveCallbacks after a clear: %d, want 1", fixture.LiveCallbacks())
	}
	_, err = fixture.NodeMeasure(ctx, a, 1, 1)
	wantStatus("NodeMeasure a after the clear", err, fixture.StatusInvalidArgument)
	ok("set probe sees the new func; clear probe sees the old")

	// A failed set leaves the registry as it was.
	must("NodeDestroy a", fixture.NodeDestroy(ctx, a))
	log = nil
	err = fixture.NodeSetMeasure(ctx, a, measure("stale", 0, 0))
	wantStatus("NodeSetMeasure on a destroyed node", err, fixture.StatusStaleHandle)
	if len(log) != 0 || fixture.LiveCallbacks() != 1 {
		fail("a failed set: ran %v, live %d; want [] and 1", log, fixture.LiveCallbacks())
	}
	ok("failed set leaves the registry as it was")

	// An unknown key gives C the default (no write: NaN), and no Go code
	// runs; a known key runs its own func.
	c, err := fixture.NodeCreate(ctx, 3)
	must("NodeCreate c", err)
	log = nil
	p, err = fixture.NodeMeasureAs(ctx, b, c)
	if err != nil || !math.IsNaN(float64(p.X)) || !math.IsNaN(float64(p.Y)) || len(log) != 0 {
		fail("NodeMeasureAs b as c (no entry): %+v %v ran %v, want NaN NaN and no Go code", p, err, log)
	}
	p, err = fixture.NodeMeasureAs(ctx, b, a)
	if err != nil || !math.IsNaN(float64(p.X)) || len(log) != 0 {
		fail("NodeMeasureAs b as a destroyed node: %+v %v ran %v, want NaN and no Go code", p, err, log)
	}
	must("NodeSetMeasure c", fixture.NodeSetMeasure(ctx, c, measure("c", 5, 6)))
	log = nil
	p, err = fixture.NodeMeasureAs(ctx, b, c)
	if err != nil || p != (fixture.Point{X: 5, Y: 6}) || strings.Join(log, " ") != "c" {
		fail("NodeMeasureAs b as c: %+v %v ran %v, want {5 6} [c]", p, err, log)
	}
	ok("unknown key gives C the default")

	// Ends: a node destroy and a context destroy release their entries.
	before := fixture.LiveCallbacks()
	must("NodeDestroy c", fixture.NodeDestroy(ctx, c))
	if fixture.LiveCallbacks() != before-1 {
		fail("LiveCallbacks after NodeDestroy: %d, want %d", fixture.LiveCallbacks(), before-1)
	}
	ok("node destroy releases its entry")
	before = fixture.LiveCallbacks()
	ctx2, err := fixture.ContextCreate(fixture.ContextOptions{NodeCapacity: 4})
	must("ContextCreate 2", err)
	for i := range 3 {
		nd, err := fixture.NodeCreate(ctx2, uint32(i))
		must("NodeCreate in ctx2", err)
		must("NodeSetMeasure in ctx2", fixture.NodeSetMeasure(ctx2, nd, measure("ctx2", 0, 0)))
	}
	if fixture.LiveCallbacks() != before+3 {
		fail("LiveCallbacks with ctx2: %d, want %d", fixture.LiveCallbacks(), before+3)
	}
	must("ContextDestroy ctx2", fixture.ContextDestroy(ctx2))
	if fixture.LiveCallbacks() != before {
		fail("LiveCallbacks after ContextDestroy: %d, want %d", fixture.LiveCallbacks(), before)
	}
	ok("context destroy releases its entries")

	// Nesting: a call-scoped call inside a registered callback, and one
	// inside a call-scoped callback.
	d, err := fixture.NodeCreate(ctx, 4)
	must("NodeCreate d", err)
	var inner int64
	must("NodeSetMeasure d", fixture.NodeSetMeasure(ctx, d, func(float32, uint32, float32, uint32) [2]float32 {
		t, err := fixture.ContextRepeat(ctx, 2, func([]byte) int64 { return 3 })
		if err != nil {
			fail("the nested ContextRepeat: %v", err)
		}
		inner += t
		return [2]float32{1, 1}
	}))
	if inner != 6 {
		fail("the nested call in the set probe took %d, want 6", inner)
	}
	outerCalls := 0
	taken, err = fixture.ContextRepeat(ctx, 3, func([]byte) int64 {
		outerCalls++
		if outerCalls == 1 {
			t, err := fixture.ContextRepeat(ctx, 2, func([]byte) int64 { return 10 })
			if err != nil || t != 20 {
				fail("the nested ContextRepeat: %d %v, want 20", t, err)
			}
		}
		return 1
	})
	if err != nil || taken != 3 || outerCalls != 3 {
		fail("the outer ContextRepeat: %d %v after %d calls, want 3 after 3", taken, err, outerCalls)
	}
	ok("nested call-scoped calls")

	// 10,000 registrations and invocations with fresh closures.
	base := fixture.LiveCallbacks()
	for i := range 10000 {
		ran := -1
		if i%2 == 0 {
			must("NodeSetMeasure", fixture.NodeSetMeasure(ctx, d, func(float32, uint32, float32, uint32) [2]float32 {
				ran = i
				return [2]float32{float32(i), 0}
			}))
			if ran != i {
				fail("registration %d: the set probe ran %d", i, ran)
			}
			if p, err := fixture.NodeMeasure(ctx, d, 0, 0); err != nil || p.X != float32(i) {
				fail("registration %d: NodeMeasure %+v %v", i, p, err)
			}
		} else {
			t, err := fixture.ContextRepeat(ctx, 1, func([]byte) int64 { ran = i; return int64(i) })
			if err != nil || t != int64(i) || ran != i {
				fail("call %d: ContextRepeat %d %v ran %d", i, t, err, ran)
			}
		}
	}
	if fixture.LiveCallbacks() != base {
		fail("LiveCallbacks after 10,000 registrations: %d, want %d", fixture.LiveCallbacks(), base)
	}
	left := slotsLeft()
	used := maxSlots - left
	fmt.Printf("slots used %d, left %d\n", used, left)
	if used < 0 || used > boundTypedefs {
		fail("the binding used %d purego callback slots for %d bound typedefs", used, boundTypedefs)
	}
	ok("at most one slot per bound typedef")
}

// exhaust uses up purego's callback slots, then calls Load: Load returns
// *CallbackSlotsError, the package stays unloaded, and the library was not
// opened, so no C function was reached.
func exhaust(lib string) {
	made := slotsLeft()
	fmt.Printf("slots made before Load %d\n", made)
	if made != maxSlots {
		fail("made %d purego callbacks, want %d", made, maxSlots)
	}
	err := fixture.Load(lib)
	var se *fixture.CallbackSlotsError
	if !errors.As(err, &se) || se.Typedef != "fx_visit_callback" {
		fail("Load with no slot left: %v, want *CallbackSlotsError for fx_visit_callback", err)
	}
	ok("load returns the slot error")
	if _, err := fixture.ContextCreate(fixture.ContextOptions{NodeCapacity: 4}); err != fixture.ErrNotLoaded {
		fail("a call after the slot error: %v, want ErrNotLoaded", err)
	}
	ok("not loaded after the slot error")
	if loaded(lib) {
		fail("the library is loaded after the slot error")
	}
	ok("no C function reached")
}

// The model mode. Each command is one line; the driver prints one line per
// command: "ran=<ids> res=<result> live=<LiveCallbacks()>", where ids are
// the funcs that ran, in order, comma-separated. A measure func f returns
// {f, 0}; when it has a nest (nk > 0) it first calls ContextRepeat of its
// node's context nk times with the writer nw. A writer w returns 1; when
// it has a nest, its first invocation first calls ContextRepeat nk times
// with the writer nw.
//
//	reset              destroy every live context
//	ctx                create a context (capacity 4)
//	ctxd C             destroy context C
//	node C             create a node in context C
//	noded N            destroy node N
//	set N F NK NW      set the measure of node N to func F (nest NK, NW)
//	clear N            clear the measure of node N
//	inv N              fx_node_measure of node N: res is X, nan or err:<status>
//	as N M             fx_node_measure_as of node N with the key of node M
//	all C              fx_context_measure_all of context C: res is the count
//	rep C K W NK NW    fx_context_repeat of context C, K times with writer W: res is the sum
type modelState struct {
	ctxs  []*fixture.Context
	nodes []nodeRef
	ran   []string
}

type nodeRef struct {
	ctx  int
	node fixture.Node
}

func (s *modelState) measureFunc(id string, ctx int, nk int, nw string) fixture.MeasureCallback {
	return func(float32, uint32, float32, uint32) [2]float32 {
		s.ran = append(s.ran, id)
		if nk > 0 {
			_, _ = fixture.ContextRepeat(s.ctxs[ctx], uint32(nk), s.writer(nw, ctx, 0, ""))
		}
		x, _ := strconv.Atoi(id)
		return [2]float32{float32(x), 0}
	}
}

func (s *modelState) writer(id string, ctx int, nk int, nw string) fixture.WriteCallback {
	first := true
	return func([]byte) int64 {
		s.ran = append(s.ran, id)
		if first && nk > 0 {
			first = false
			_, _ = fixture.ContextRepeat(s.ctxs[ctx], uint32(nk), s.writer(nw, ctx, 0, ""))
		}
		return 1
	}
}

func result(err error) string {
	var st fixture.Status
	switch {
	case errors.As(err, &st):
		return "err:" + strconv.Itoa(int(st))
	case err == fixture.ErrDestroyed:
		return "err:destroyed"
	}
	return "err:" + err.Error()
}

func model(lib string) {
	must("Load", fixture.Load(lib))
	s := &modelState{}
	in := bufio.NewScanner(os.Stdin)
	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()
	for in.Scan() {
		f := strings.Fields(in.Text())
		if len(f) == 0 {
			continue
		}
		num := func(i int) int {
			v, err := strconv.Atoi(f[i])
			if err != nil {
				fail("command %q: %v", in.Text(), err)
			}
			return v
		}
		s.ran = nil
		res := ""
		switch f[0] {
		case "reset":
			for _, c := range s.ctxs {
				// A context that ctxd destroyed is ErrDestroyed.
				if err := fixture.ContextDestroy(c); err != nil && err != fixture.ErrDestroyed {
					fail("ContextDestroy: %v", err)
				}
			}
			s.ctxs, s.nodes = nil, nil
		case "ctx":
			c, err := fixture.ContextCreate(fixture.ContextOptions{NodeCapacity: 4})
			must("ContextCreate", err)
			s.ctxs = append(s.ctxs, c)
			res = strconv.Itoa(len(s.ctxs) - 1)
		case "ctxd":
			if err := fixture.ContextDestroy(s.ctxs[num(1)]); err != nil {
				res = result(err)
			}
		case "node":
			c := num(1)
			n, err := fixture.NodeCreate(s.ctxs[c], 0)
			if err != nil {
				res = result(err)
				break
			}
			s.nodes = append(s.nodes, nodeRef{ctx: c, node: n})
			res = strconv.Itoa(len(s.nodes) - 1)
		case "noded":
			n := s.nodes[num(1)]
			if err := fixture.NodeDestroy(s.ctxs[n.ctx], n.node); err != nil {
				res = result(err)
			}
		case "set":
			n := s.nodes[num(1)]
			if err := fixture.NodeSetMeasure(s.ctxs[n.ctx], n.node, s.measureFunc(f[2], n.ctx, num(3), f[4])); err != nil {
				res = result(err)
			}
		case "clear":
			n := s.nodes[num(1)]
			if err := fixture.NodeSetMeasure(s.ctxs[n.ctx], n.node, nil); err != nil {
				res = result(err)
			}
		case "inv", "as":
			n := s.nodes[num(1)]
			var p fixture.Point
			var err error
			if f[0] == "inv" {
				p, err = fixture.NodeMeasure(s.ctxs[n.ctx], n.node, 0, 0)
			} else {
				p, err = fixture.NodeMeasureAs(s.ctxs[n.ctx], n.node, s.nodes[num(2)].node)
			}
			switch {
			case err != nil:
				res = result(err)
			case math.IsNaN(float64(p.X)):
				res = "nan"
			default:
				res = strconv.Itoa(int(p.X))
			}
		case "all":
			cnt, err := fixture.ContextMeasureAll(s.ctxs[num(1)], 0, 0)
			if err != nil {
				res = result(err)
			} else {
				res = strconv.Itoa(int(cnt))
			}
		case "rep":
			c := num(1)
			t, err := fixture.ContextRepeat(s.ctxs[c], uint32(num(2)), s.writer(f[3], c, num(4), f[5]))
			if err != nil {
				res = result(err)
			} else {
				res = strconv.FormatInt(t, 10)
			}
		default:
			fail("unknown command %q", in.Text())
		}
		fmt.Fprintf(out, "ran=%s res=%s live=%d\n", strings.Join(s.ran, ","), res, fixture.LiveCallbacks())
	}
	if err := in.Err(); err != nil {
		fail("stdin: %v", err)
	}
}
