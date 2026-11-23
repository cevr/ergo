// Command main is the stock-compiled program that imports the Ergo
// package tiny (the producer's archive, or the stock reference under the
// same path) and drives each contract once, printing one line per
// contract. The runner links it twice and compares the two outputs.
package main

import (
	"fmt"
	"os"
	"runtime"
	"weak"

	"ergo.local/nativeobj/dep"
	"ergo.local/nativeobj/tiny"
)

var held weak.Pointer[int]

// fresh is a heap int whose only strong reference is the one it returns.
//
//go:noinline
func fresh(v int) *int {
	p := new(int)
	*p = v
	held = weak.Make(p)
	return p
}

// grow uses about n*300 bytes of stack, so the goroutine's stack is
// copied to a larger one while its callers wait in calls.
//
//go:noinline
func grow(n int) int {
	var pad [256]byte
	pad[n%256] = byte(n)
	if n == 0 {
		return int(pad[0])
	}
	return grow(n-1) + int(pad[(n+1)%256]&1)
}

type holder struct{ p *int }

func main() {
	runtime.GOMAXPROCS(2)

	fmt.Println("diff", tiny.Diff(7, 3), tiny.Diff(-5, 11))
	fmt.Println("twice", tiny.Twice(21), tiny.Twice(-4))

	k := 10
	fmt.Println("apply", tiny.Apply(func(x int) int { return x*3 + k }, 4))

	// Only Hold's frame holds the int while the callback collects: an
	// unmarked slot lets the collector free it and clear the weak pointer.
	got := tiny.Hold(fresh(40), func(x int) int {
		runtime.GC()
		runtime.GC()
		if held.Value() == nil {
			return -1000
		}
		return x
	})
	fmt.Println("hold", got)

	// The callback grows the stack: Anchor's pointer to its own v must be
	// moved with the frame, or the store after the call misses v.
	fmt.Println("anchor", tiny.Anchor(func(x int) int { return x + 70 + grow(4000) }, 5))

	// Publish heap pointers while the collector runs concurrently (the
	// runner sets GODEBUG=gccheckmark=1, which verifies every mark).
	h := &holder{}
	done := make(chan bool)
	go func() {
		for i := 0; i < 20; i++ {
			runtime.GC()
		}
		done <- true
	}()
	sum := 0
	for i := 0; i < 200000; i++ {
		q := new(int)
		*q = i
		tiny.Publish(&h.p, q)
		sum += *h.p
	}
	<-done
	runtime.GC()
	fmt.Println("publish", *h.p, sum)

	// Spin has no call: only an asynchronous preemption lets the
	// collector stop the world while it runs. If none can, GC never
	// returns and the runner's deadline names the hang.
	res := make(chan int)
	started := make(chan bool)
	go func() {
		started <- true
		res <- tiny.Spin()
	}()
	<-started
	for i := 0; i < 50; i++ {
		runtime.GC()
	}
	tiny.Stop()
	<-res
	fmt.Println("spin stopped")

	fmt.Println("init", tiny.InitRuns(), dep.Inits())
	fmt.Println("round trip complete")
	os.Exit(0)
}
