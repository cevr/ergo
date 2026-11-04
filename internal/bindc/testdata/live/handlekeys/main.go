// Command handlekeys runs the scenarios of the bind-c-2 verifier's
// handle-key probes (2b M1 and M2) against the typed forms that keep one
// key per live registration (TestLiveCallbackHandleKeys). It prints
// "ok: <check>" for each check that holds and "FAIL: <check>" otherwise.
package main

import (
	"fmt"
	"os"
	"sync/atomic"
	"time"

	"ergo.local/bind/cb"
)

func check(ok bool, name string, args ...any) {
	if ok {
		fmt.Println("ok: " + name)
		return
	}
	fmt.Printf("FAIL: %s %v\n", name, args)
}

func main() {
	if err := cb.Load(os.Args[1]); err != nil {
		fmt.Println("FAIL: Load:", err)
		os.Exit(1)
	}
	h, err := cb.GCreate()
	if err != nil {
		fmt.Println("FAIL: GCreate:", err)
		os.Exit(1)
	}
	n := cb.GNode{ContextId: 1, Slot: 2, Generation: 3}
	ran := ""
	mk := func(s string) func(float32) { return func(float32) { ran += s } }

	// S1: two positions of g_two, each with its own typedef.
	_ = cb.GTwo(h, n, mk("a"), mk("b"))
	_ = cb.GCallA(h, n)
	check(ran == "a", "S1 C calls a: Go runs a", ran)
	ran = ""
	_ = cb.GCallB(h, n)
	check(ran == "b", "S1 C calls b: Go runs b", ran)

	// S2: a clear of b keeps a.
	ran = ""
	_ = cb.GTwo(h, n, mk("a"), nil)
	_ = cb.GCallA(h, n)
	check(ran == "a", "S2 clear of b keeps a", ran)
	check(cb.LiveCallbacks() == 1, "S2 live 1", cb.LiveCallbacks())

	// S3: a failed call leaves the registry as it was.
	_ = cb.GTwo(h, n, nil, nil)
	before := cb.LiveCallbacks()
	_ = cb.GFailNext(h)
	err = cb.GTwo(h, n, mk("a"), mk("b"))
	check(err != nil && cb.LiveCallbacks() == before, "S3 failed GTwo: live unchanged", err, before, cb.LiveCallbacks())
	_ = cb.GTwo(h, n, mk("a"), nil)
	_ = cb.GFailNext(h)
	_ = cb.GTwo(h, n, mk("A2"), nil)
	ran = ""
	_ = cb.GCallA(h, n)
	check(ran == "a", "S3 failed replacement: the old a runs", ran)
	_ = cb.GTwo(h, n, nil, nil)

	// S4: one typedef per setter.
	_ = cb.GSetX(h, n, mk("x"))
	_ = cb.GSetY(h, n, mk("y"))
	ran = ""
	_ = cb.GCallX(h, n)
	check(ran == "x", "S4 C calls x: Go runs x", ran)
	ran = ""
	_ = cb.GCallY(h, n)
	check(ran == "y", "S4 C calls y: Go runs y", ran)
	_ = cb.GSetX(h, n, nil)
	_ = cb.GSetY(h, n, nil)

	// Nesting on one key: the inner call replaces the entry and restores
	// the outer one when it returns.
	var f1n, f2n int
	f2 := func(uint32) int32 { f2n++; return 2 }
	var f1 func(uint32) int32
	f1 = func(uint32) int32 {
		f1n++
		if f1n == 1 {
			if err := cb.GVisit(h, n, f2); err != nil {
				fmt.Println("FAIL: nested GVisit:", err)
			}
		}
		return 1
	}
	err = cb.GVisit(h, n, f1)
	check(err == nil && f1n == 2 && f2n == 2, "nest: f1 2 f2 2", err, f1n, f2n)
	check(cb.LiveCallbacks() == 0, "nest: live 0", cb.LiveCallbacks())

	// The overlap probe through the Owner: the second goroutine's call
	// begins only after the first one's returned, so the calls on one key
	// never overlap.
	o := cb.NewOwner()
	in1, rel, in2 := make(chan bool, 1), make(chan bool), make(chan bool, 1)
	var g1n, g2n atomic.Int32
	g1 := func(uint32) int32 {
		if g1n.Add(1) == 1 {
			in1 <- true
			<-rel
		}
		return 1
	}
	g2 := func(uint32) int32 {
		if g2n.Add(1) == 1 {
			in2 <- true
		}
		return 2
	}
	done1, done2 := make(chan error, 1), make(chan error, 1)
	go func() {
		done1 <- o.Do(func() {
			if err := cb.GVisit(h, n, g1); err != nil {
				fmt.Println("FAIL: GVisit g1:", err)
			}
		})
	}()
	<-in1
	go func() {
		done2 <- o.Do(func() {
			if err := cb.GVisit(h, n, g2); err != nil {
				fmt.Println("FAIL: GVisit g2:", err)
			}
		})
	}()
	began := false
	select {
	case <-in2:
		began = true
	case <-time.After(300 * time.Millisecond):
	}
	rel <- true
	e1 := <-done1
	e2 := <-done2
	check(!began, "owner: the second call began after the first returned")
	check(e1 == nil && e2 == nil && g1n.Load() == 2 && g2n.Load() == 2, "owner: f1 2 f2 2", e1, e2, g1n.Load(), g2n.Load())
	check(cb.LiveCallbacks() == 0, "owner: live 0", cb.LiveCallbacks())
	if err := o.Close(); err != nil {
		fmt.Println("FAIL: Close:", err)
	}
}
