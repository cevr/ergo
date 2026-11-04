// Command driver drives the fixture library through the generated typed
// layer for TestLive. It takes the paths of the good library, the library
// with ABI version 3 and the library without fx_node_count, prints "ok:
// <check>" for each check, and exits 1 at the first failed check.
package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"

	"ergo.local/bind/fixture"
)

func fail(format string, args ...any) {
	fmt.Printf("FAIL: "+format+"\n", args...)
	os.Exit(1)
}

func ok(check string) { fmt.Println("ok: " + check) }

// must fails on a non-nil error.
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

func main() {
	// The context belongs to the OS thread that creates it.
	runtime.LockOSThread()
	if len(os.Args) != 4 {
		fail("usage: driver <good> <abi3> <omit>")
	}
	good, abi3, omit := os.Args[1], os.Args[2], os.Args[3]

	if _, err := fixture.ContextCreate(fixture.ContextOptions{NodeCapacity: 4}); err != fixture.ErrNotLoaded {
		fail("a call before Load: %v, want ErrNotLoaded", err)
	}
	ok("not loaded before Load")

	var mismatch *fixture.ABIMismatchError
	if err := fixture.Load(abi3); !errors.As(err, &mismatch) || mismatch.Want != 2 || mismatch.Got != 3 {
		fail("Load of the ABI 3 library: %v, want *ABIMismatchError{Want: 2, Got: 3}", err)
	}
	ok("abi mismatch")
	// The ABI 3 library would create a context; the binding must not call it.
	if _, err := fixture.ContextCreate(fixture.ContextOptions{NodeCapacity: 4}); err != fixture.ErrNotLoaded {
		fail("a call after the ABI mismatch: %v, want ErrNotLoaded", err)
	}
	ok("not loaded after the abi mismatch")

	if err := fixture.Load(omit); err == nil || !strings.Contains(err.Error(), "fx_node_count") {
		fail("Load of the library without fx_node_count: %v, want an error that names it", err)
	}
	ok("missing symbol")
	if _, err := fixture.AbiVersion(); err != fixture.ErrNotLoaded {
		fail("a call after the missing symbol: %v, want ErrNotLoaded", err)
	}
	ok("not loaded after the missing symbol")

	must("Load", fixture.Load(good))
	if v, err := fixture.AbiVersion(); err != nil || v != fixture.FX_ABI_VERSION {
		fail("AbiVersion: %d, %v", v, err)
	}
	ok("load")

	// fx_context_create returns FX_UNSUPPORTED_VERSION unless struct_size
	// and abi_version are right: the binding sets them.
	ctx, err := fixture.ContextCreate(fixture.ContextOptions{NodeCapacity: 4, Strict: true})
	must("ContextCreate", err)
	ok("autofill checked by C")

	node, err := fixture.NodeCreate(ctx, 7)
	must("NodeCreate", err)
	if node.Generation == 0 {
		fail("NodeCreate: generation 0")
	}
	ok("node")

	must("NodeSetName", fixture.NodeSetName(ctx, node, []byte("hello")))
	buf := make([]byte, 16)
	n, err := fixture.NodeReadName(ctx, node, buf)
	if err != nil || n != 5 || string(buf[:n]) != "hello" {
		fail("NodeReadName: %d %q %v", n, buf[:n], err)
	}
	short := make([]byte, 3)
	if n, err := fixture.NodeReadName(ctx, node, short); err != nil || n != 3 || string(short) != "hel" {
		fail("NodeReadName into 3 bytes: %d %q %v", n, short, err)
	}
	ok("input buffer round trip")

	must("NodeSetName empty", fixture.NodeSetName(ctx, node, nil))
	if n, err := fixture.NodeReadName(ctx, node, buf); err != nil || n != 0 {
		fail("NodeReadName after an empty name: %d %v", n, err)
	}
	ok("empty input buffer")
	if n, err := fixture.NodeReadName(ctx, node, nil); err != nil || n != 0 {
		fail("NodeReadName into nil: %d %v", n, err)
	}
	ok("empty output buffer")

	if c, err := fixture.NodeCount(ctx); err != nil || c != 1 {
		fail("NodeCount: %d %v", c, err)
	}
	ok("out-parameter")

	box := fixture.Box{Kind: 3, Origin: [2]float64{1.5, -2.25}, Corner: fixture.Point{X: 3, Y: 4},
		Color: [4]uint16{1, 2, 65535, 4}, Layer: -2, Depth: -300, Z: 1 << 40, Visible: true, Age: 9.5}
	copy(box.Name[:], "name")
	must("NodeSetBox", fixture.NodeSetBox(ctx, node, box))
	got, err := fixture.NodeGetBox(ctx, node)
	must("NodeGetBox", err)
	if got.Kind != box.Kind || got.Origin != box.Origin || got.Corner != box.Corner || got.Color != box.Color ||
		got.Layer != box.Layer || got.Depth != box.Depth || got.Z != box.Z || got.Visible != box.Visible ||
		got.Age != box.Age || !bytes.Equal(got.Name[:], box.Name[:]) {
		fail("NodeGetBox: %+v, want %+v", got, box)
	}
	ok("struct round trip")

	if l, err := fixture.PointLength(fixture.Point{X: 3, Y: 4}); err != nil || l != 5 {
		fail("PointLength: %v %v", l, err)
	}
	ok("struct by value")

	done := make(chan error)
	go func() {
		runtime.LockOSThread()
		_, err := fixture.NodeCount(ctx)
		done <- err
	}()
	wantStatus("NodeCount on another OS thread", <-done, fixture.StatusWrongThread)
	ok("wrong thread")

	must("NodeDestroy", fixture.NodeDestroy(ctx, node))
	wantStatus("NodeDestroy of a destroyed node", fixture.NodeDestroy(ctx, node), fixture.StatusStaleHandle)
	wantStatus("NodeSetName of a destroyed node", fixture.NodeSetName(ctx, node, []byte("x")), fixture.StatusStaleHandle)
	ok("stale handle")

	must("ContextDestroy", fixture.ContextDestroy(ctx))
	ok("destroy")
	// The context's memory is freed: a call that reached C would read it.
	if _, err := fixture.NodeCount(ctx); err != fixture.ErrDestroyed {
		fail("NodeCount after ContextDestroy: %v, want ErrDestroyed", err)
	}
	ok("call after destroy")
	if err := fixture.ContextDestroy(ctx); err != fixture.ErrDestroyed {
		fail("ContextDestroy twice: %v, want ErrDestroyed", err)
	}
	ok("destroy after destroy")
}
