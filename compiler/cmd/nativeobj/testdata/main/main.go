// Command main is the stock-compiled program that imports the Ergo
// package tiny (the producer's archive, or the stock reference under the
// same path) and drives each contract once, printing one line per
// contract. The runner links it twice and compares the two outputs.
package main

import (
	"fmt"
	"os"
	"path/filepath"
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

func callbackFrame(wantFile string) {
	var pcs [16]uintptr
	n := runtime.Callers(2, pcs[:])
	frames := runtime.CallersFrames(pcs[:n])
	for {
		f, more := frames.Next()
		if f.Function == "ergo.local/nativeobj/tiny.Apply" {
			if filepath.Base(f.File) == wantFile && f.Line > 0 {
				return
			}
			fmt.Fprintf(os.Stderr, "callback frame: got %s:%d, want %s with a source line\n", f.File, f.Line, wantFile)
			os.Exit(1)
		}
		if !more {
			fmt.Fprintln(os.Stderr, "callback frame: tiny.Apply is missing")
			os.Exit(1)
		}
	}
}

func memoryChecks(callbackFile string) {
	fmt.Println("diff", tiny.Diff(7, 3), tiny.Diff(-5, 11))
	fmt.Println("twice", tiny.Twice(21), tiny.Twice(-4))
	fmt.Println("combine", tiny.Combine(7, 3), tiny.Combine(-5, 11))

	k := 10
	fmt.Println("apply", tiny.Apply(func(x int) int {
		callbackFrame(callbackFile)
		return x*3 + k
	}, 4))

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
}

func preemption() {
	// Spin has no call: only an asynchronous preemption lets the
	// collector stop the world while it runs. Wait for an event inside
	// Spin, not an announcement that its caller is about to enter it.
	res := make(chan int)
	go func() { res <- tiny.Spin() }()
	for tiny.SpinStarted() == 0 {
		runtime.Gosched()
	}
	runtime.GC()
	tiny.Stop()
	<-res
	fmt.Println("spin stopped")
}

func main() {
	runtime.GOMAXPROCS(2)
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "want fixture phase and callback source")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "memory":
		memoryChecks(os.Args[2])
	case "preempt":
		preemption()
	default:
		fmt.Fprintln(os.Stderr, "unknown fixture phase", os.Args[1])
		os.Exit(2)
	}
	// Tiny's init increments only by the dependency's count as it was
	// during init, so an early run yields zero and a repeated run two.
	tinyCount, depCount, trace := tiny.InitRuns(), dep.Inits(), tiny.InitTrace()
	if tinyCount != 1 || depCount != 1 || trace != 12 {
		fmt.Fprintf(os.Stderr, "init counts: tiny=%d dep=%d trace=%d; want 1, 1, 12\n", tinyCount, depCount, trace)
		os.Exit(1)
	}
	fmt.Println("init", tinyCount, depCount, trace)
	fmt.Println("round trip complete")
}
