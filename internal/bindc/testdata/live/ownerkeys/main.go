// Command ownerkeys drives a binding whose handle key does not name its
// owner (TestLiveCallbackOwnerKeys; bind-c-2 r2, finding 2b M1): the key
// (slot, generation) of g_m and g_v repeats from one g_h to the next, and
// g_h has thread owner. Each handle lives on the thread of an Owner. It
// prints "ok: <check>" for each check that holds and exits 1 at the first
// one that fails.
//
//	ownerkeys <lib> <seed> <n>
//
// First the verifier's scenarios: two owner threads with equal key values,
// call-scoped calls on two threads that overlap, two handles of one thread
// with equal key values, and calls from a thread that did not create the
// handle. Then n generated command sequences against a reference model.
package main

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"slices"
	"strconv"
	"sync/atomic"

	"ergo.local/bind/cb"
)

func fail(format string, args ...any) {
	fmt.Printf("FAIL: "+format+"\n", args...)
	os.Exit(1)
}

func ok(check string) { fmt.Println("ok: " + check) }

// do runs f on the thread of o.
func do(o *cb.Owner, f func()) {
	if err := o.Do(f); err != nil {
		fail("Do: %v", err)
	}
}

// ran is the log of the Go functions that ran, by name; the callbacks run
// in a function of Owner.Do, and Do returns after it.
var ran []string

func m(name string) cb.GM { return func(float32) { ran = append(ran, name) } }

// create makes a handle on the thread of o.
func create(o *cb.Owner) *cb.GH {
	var h *cb.GH
	var err error
	do(o, func() { h, err = cb.GCreate() })
	if err != nil {
		fail("GCreate: %v", err)
	}
	return h
}

// cCalls is the number of g_set and g_visit calls C saw for h.
func cCalls(o *cb.Owner, h *cb.GH) uint32 {
	var n uint32
	var err error
	do(o, func() { n, err = cb.GCalls(h) })
	if err != nil {
		fail("GCalls: %v", err)
	}
	return n
}

// call makes C of h call its g_m for n, and returns what Go ran.
func call(o *cb.Owner, h *cb.GH, n cb.GK) ([]string, error) {
	ran = nil
	var err error
	do(o, func() { err = cb.GCall(h, n) })
	return ran, err
}

func want(cond bool, format string, args ...any) {
	if !cond {
		fail(format, args...)
	}
}

func main() {
	if len(os.Args) != 4 {
		fail("usage: ownerkeys <lib> <seed> <n>")
	}
	if err := cb.Load(os.Args[1]); err != nil {
		fail("Load: %v", err)
	}
	seed, err1 := strconv.ParseUint(os.Args[2], 10, 64)
	count, err2 := strconv.Atoi(os.Args[3])
	if err1 != nil || err2 != nil {
		fail("seed %q, n %q", os.Args[2], os.Args[3])
	}
	o1, o2 := cb.NewOwner(), cb.NewOwner()
	n := cb.GK{Slot: 1, Generation: 1}

	// The verifier's registered scenario on two owner threads.
	h1, h2 := create(o1), create(o2)
	do(o1, func() { want(cb.GSet(h1, n, m("A")) == nil, "GSet h1 A") })
	do(o2, func() { want(cb.GSet(h2, n, m("B")) == nil, "GSet h2 B") })
	got, err := call(o1, h1, n)
	want(err == nil && slices.Equal(got, []string{"A"}), "two threads: C of h1 calls its callback: Go ran %v %v, want [A]", got, err)
	got, err = call(o2, h2, n)
	want(err == nil && slices.Equal(got, []string{"B"}), "two threads: C of h2 calls its callback: Go ran %v %v, want [B]", got, err)
	ok("two threads, one key value: h1 runs A and h2 runs B")
	do(o2, func() { want(cb.GDestroy(h2) == nil, "GDestroy h2") })
	got, err = call(o1, h1, n)
	want(err == nil && slices.Equal(got, []string{"A"}) && cb.LiveCallbacks() == 1,
		"two threads: after GDestroy(h2), C of h1 calls its callback: Go ran %v %v, live %d; want [A] and 1", got, err, cb.LiveCallbacks())
	ok("two threads: after GDestroy(h2), h1 still runs A and LiveCallbacks is 1")

	// Call scope on two threads that overlap: each call runs its own func.
	h3 := create(o2)
	in1, relA, in2, relB := make(chan bool), make(chan bool), make(chan bool), make(chan bool)
	var f1n, f2n atomic.Int32
	f1 := func(uint32) int32 {
		if f1n.Add(1) == 1 {
			in1 <- true
			<-relA
		}
		return 1
	}
	f2 := func(uint32) int32 {
		if f2n.Add(1) == 1 {
			in2 <- true
			<-relB
		}
		return 2
	}
	done1, done2 := make(chan error), make(chan error)
	go func() { _ = o1.Do(func() { done1 <- cb.GVisit(h1, n, f1) }) }()
	<-in1
	go func() { _ = o2.Do(func() { done2 <- cb.GVisit(h3, n, f2) }) }()
	<-in2
	relA <- true
	e1 := <-done1
	relB <- true
	e2 := <-done2
	want(e1 == nil && e2 == nil && f1n.Load() == 2 && f2n.Load() == 2 && cb.LiveCallbacks() == 1,
		"two threads, call scope: f1 ran %d, f2 ran %d, errs %v %v, live %d; want 2, 2, nil, nil and 1", f1n.Load(), f2n.Load(), e1, e2, cb.LiveCallbacks())
	ok("two threads, call scope: overlapping calls with one key value each run their own func")
	do(o2, func() { want(cb.GDestroy(h3) == nil, "GDestroy h3") })

	// Two handles of one thread with one key value: the second is refused.
	h4 := create(o1)
	before := cCalls(o1, h4)
	do(o1, func() {
		err := cb.GSet(h4, n, m("B"))
		want(errors.Is(err, cb.ErrKeyInUse), "one thread: GSet(h4) under h1's key: %v, want ErrKeyInUse", err)
		err = cb.GSet(h4, n, nil)
		want(errors.Is(err, cb.ErrKeyInUse), "one thread: a clear by h4 under h1's key: %v, want ErrKeyInUse", err)
	})
	want(cCalls(o1, h4) == before && cb.LiveCallbacks() == 1, "one thread: after the refusals C saw %d calls (want %d), live %d (want 1)",
		cCalls(o1, h4), before, cb.LiveCallbacks())
	got, err = call(o1, h1, n)
	want(err == nil && slices.Equal(got, []string{"A"}), "one thread: h1 after the refusals: Go ran %v %v, want [A]", got, err)
	ok("one thread, one key value: a second handle gets ErrKeyInUse, no C call, and h1 still runs A")
	do(o1, func() {
		want(cb.GDrop(h4, n) == nil, "GDrop h4")
		want(cb.GDestroy(h4) == nil, "GDestroy h4")
	})
	got, err = call(o1, h1, n)
	want(err == nil && slices.Equal(got, []string{"A"}) && cb.LiveCallbacks() == 1,
		"one thread: after GDrop(h4) and GDestroy(h4), h1: Go ran %v %v, live %d; want [A] and 1", got, err, cb.LiveCallbacks())
	ok("one thread: an end and a destroy of the second handle keep h1's registration")
	h5 := create(o1)
	var inner error
	gRan, fRan := 0, 0
	do(o1, func() {
		err = cb.GVisit(h1, n, func(uint32) int32 {
			fRan++
			if fRan == 1 {
				inner = cb.GVisit(h5, n, func(uint32) int32 { gRan++; return 0 })
			}
			return 1
		})
	})
	want(err == nil && errors.Is(inner, cb.ErrKeyInUse) && fRan == 2 && gRan == 0 && cb.LiveCallbacks() == 1,
		"one thread, nested: outer %v ran %d, inner %v ran %d, live %d; want nil 2, ErrKeyInUse 0, live 1", err, fRan, inner, gRan, cb.LiveCallbacks())
	ok("one thread, call scope: a nested call of another handle with one key value gets ErrKeyInUse")
	do(o1, func() { want(cb.GDestroy(h5) == nil, "GDestroy h5") })

	// A thread that did not create the handle: nothing changes.
	before = cCalls(o1, h1)
	visited := 0
	for _, w := range []struct {
		what string
		f    func() error
	}{
		{"GSet", func() error { return cb.GSet(h1, n, m("C")) }},
		{"a clear", func() error { return cb.GSet(h1, n, nil) }},
		{"GDrop", func() error { return cb.GDrop(h1, n) }},
		{"GVisit", func() error { return cb.GVisit(h1, n, func(uint32) int32 { visited++; return 0 }) }},
	} {
		var err error
		do(o2, func() { err = w.f() })
		want(errors.Is(err, cb.ErrWrongThread), "another thread: %s of h1: %v, want ErrWrongThread", w.what, err)
	}
	got, err = call(o1, h1, n)
	want(err == nil && slices.Equal(got, []string{"A"}) && visited == 0 && cCalls(o1, h1) == before && cb.LiveCallbacks() == 1,
		"another thread: then h1 runs %v %v, the visit ran %d, C saw %d calls (want %d), live %d", got, err, visited, cCalls(o1, h1), before, cb.LiveCallbacks())
	ok("another thread: each call that changes h1's registrations gets ErrWrongThread, and nothing changes")
	do(o1, func() { want(cb.GDestroy(h1) == nil, "GDestroy h1") })
	want(cb.LiveCallbacks() == 0, "after GDestroy(h1), live %d, want 0", cb.LiveCallbacks())

	model(rand.New(rand.NewPCG(seed, 0x0c3e)), count, []*cb.Owner{o1, o2})
	for _, o := range []*cb.Owner{o1, o2} {
		if err := o.Close(); err != nil {
			fail("Close: %v", err)
		}
	}
}

// The model: handles on two owner threads, two slots, one generation. The
// registry of g_m is a map from (owner thread, slot) to (handle, func);
// C keeps one g_m per handle and slot.
type mHandle struct {
	h     *cb.GH
	owner int
	c     [2]int // C's g_m per slot: a func id, or -1
}

type mKey struct{ owner, slot int }

type mEntry struct{ handle, fn int }

func model(r *rand.Rand, count int, owners []*cb.Owner) {
	stats := map[string]int{}
	for s := range count {
		var hs []*mHandle
		reg := map[mKey]mEntry{}
		newHandle := func() *mHandle {
			x := &mHandle{owner: r.IntN(len(owners)), c: [2]int{-1, -1}}
			x.h = create(owners[x.owner])
			return x
		}
		for range 4 {
			hs = append(hs, newHandle())
		}
		steps := 1 + r.IntN(40)
		for step := range steps {
			i, slot := r.IntN(len(hs)), r.IntN(2)
			x := hs[i]
			n := cb.GK{Slot: uint32(slot), Generation: 1}
			via := x.owner
			if r.IntN(6) == 0 {
				via = 1 - x.owner
			}
			at := func(format string, args ...any) string {
				return fmt.Sprintf("sequence %d step %d (handle %d of owner %d, slot %d, via owner %d): ", s, step, i, x.owner, slot, via) + fmt.Sprintf(format, args...)
			}
			k := mKey{x.owner, slot}
			held, hasHeld := reg[k]
			switch c := r.IntN(10); {
			case c < 4: // set or clear
				fn := r.IntN(5) - 1 // -1 clears
				var f cb.GM
				if fn >= 0 {
					f = m("f" + strconv.Itoa(fn))
				}
				var err error
				do(owners[via], func() { err = cb.GSet(x.h, n, f) })
				switch {
				case via != x.owner:
					want(errors.Is(err, cb.ErrWrongThread), "%s", at("GSet: %v, want ErrWrongThread", err))
					stats["wrong thread"]++
				case hasHeld && held.handle != i:
					want(errors.Is(err, cb.ErrKeyInUse), "%s", at("GSet: %v, want ErrKeyInUse", err))
					stats["key in use"]++
				default:
					want(err == nil, "%s", at("GSet: %v", err))
					x.c[slot] = fn
					if fn >= 0 {
						reg[k] = mEntry{i, fn}
						stats["set"]++
					} else {
						delete(reg, k)
						stats["clear"]++
					}
				}
			case c < 5: // the end function
				var err error
				do(owners[via], func() { err = cb.GDrop(x.h, n) })
				if via != x.owner {
					want(errors.Is(err, cb.ErrWrongThread), "%s", at("GDrop: %v, want ErrWrongThread", err))
					stats["wrong thread"]++
					break
				}
				want(err == nil, "%s", at("GDrop: %v", err))
				x.c[slot] = -1
				if hasHeld && held.handle == i {
					delete(reg, k)
				} else if hasHeld {
					stats["end keeps another handle's entry"]++
				}
			case c < 8: // C of the handle calls its g_m
				got, err := call(owners[x.owner], x.h, n)
				var exp []string
				if x.c[slot] >= 0 && hasHeld {
					exp = []string{"f" + strconv.Itoa(held.fn)}
				}
				want((err != nil) == (x.c[slot] < 0) && slices.Equal(got, exp), "%s", at("GCall: Go ran %v (%v), want %v", got, err, exp))
				if len(exp) > 0 {
					stats["call ran"]++
				}
			case c < 9: // a call-scoped call
				runs := 0
				var err error
				do(owners[via], func() { err = cb.GVisit(x.h, n, func(uint32) int32 { runs++; return 0 }) })
				if via != x.owner {
					want(errors.Is(err, cb.ErrWrongThread) && runs == 0, "%s", at("GVisit: %v after %d runs, want ErrWrongThread", err, runs))
					stats["wrong thread"]++
					break
				}
				want(err == nil && runs == 2, "%s", at("GVisit: %v after %d runs, want nil after 2", err, runs))
			default: // destroy, and a new handle in its place
				var err error
				do(owners[x.owner], func() { err = cb.GDestroy(x.h) })
				want(err == nil, "%s", at("GDestroy: %v", err))
				for k, e := range reg {
					if e.handle == i {
						delete(reg, k)
					}
				}
				hs[i] = newHandle()
				stats["destroy"]++
			}
			want(cb.LiveCallbacks() == len(reg), "%s", at("LiveCallbacks %d, the model has %d", cb.LiveCallbacks(), len(reg)))
			for sl := range 2 {
				_, a := reg[mKey{0, sl}]
				_, b := reg[mKey{1, sl}]
				if a && b {
					stats["two owners, one key value live"]++
					break
				}
			}
		}
		for _, x := range hs {
			do(owners[x.owner], func() {
				if err := cb.GDestroy(x.h); err != nil {
					fail("GDestroy at the end: %v", err)
				}
			})
		}
		want(cb.LiveCallbacks() == 0, "sequence %d: LiveCallbacks %d after the destroys, want 0", s, cb.LiveCallbacks())
	}
	fmt.Printf("model: %d sequences, %v\n", count, stats)
	for _, k := range []string{"set", "clear", "key in use", "wrong thread", "call ran", "destroy", "two owners, one key value live", "end keeps another handle's entry"} {
		want(stats[k] > 0, "the sequences made no case of %q", k)
	}
	ok("generated sequences: each invocation ran the func the model has for its owner thread and key, and LiveCallbacks matched")
}
