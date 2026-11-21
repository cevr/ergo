// Command floor is the Go runtime's floor for benchmarks/park (PARK2): the
// least work the runtime allows for each probe, below anything a person
// writes in plain Go. A spawn and join is one goroutine start (a `go` of a
// function with no arguments, so no closure) and one park and ready of the
// joiner; a request and its reply is two parks and two readies. It keeps
// no stop: a wait that a stop also ends costs the probes more than this.
// It links runtime.gopark and runtime.goready as runtime/rt's park word
// does (go.dev/issue/67401), and prints what the Ergo probe prints.
package main

import (
	"fmt"
	"os"
	"strconv"
	"sync/atomic"
	"unsafe"
)

//go:linkname gopark runtime.gopark
func gopark(unlockf func(gp uintptr, lock unsafe.Pointer) bool, lock unsafe.Pointer, reason uint8, traceReason uint8, traceskip int)

//go:linkname goready runtime.goready
func goready(gp uintptr, traceskip int)

// A word holds empty, notified or the parked goroutine's g, as rt's does.
const (
	empty    = 0
	notified = 1
)

func park(w *uintptr) {
	if atomic.CompareAndSwapUintptr(w, notified, empty) {
		return
	}
	gopark(commit, unsafe.Pointer(w), 21, 4, 1)
}

//go:nosplit
func commit(gp uintptr, w unsafe.Pointer) bool {
	if atomic.CompareAndSwapUintptr((*uintptr)(w), empty, gp) {
		return true
	}
	atomic.StoreUintptr((*uintptr)(w), empty)
	return false
}

func unpark(w *uintptr) {
	for {
		old := atomic.LoadUintptr(w)
		switch old {
		case notified:
			return
		case empty:
			if atomic.CompareAndSwapUintptr(w, empty, notified) {
				return
			}
		default:
			if atomic.CompareAndSwapUintptr(w, old, empty) {
				goready(old, 1)
				return
			}
		}
	}
}

// The joins run one at a time, so one record carries each child's input
// and result, and the `go` statement needs no closure.
var join struct {
	in, out int
	w       uintptr
}

func child() {
	join.out = join.in & 3
	unpark(&join.w)
}

func spawn(n int) int {
	acc := 0
	for i := 0; i < n; i++ {
		join.in = i
		go child()
		park(&join.w)
		acc += join.out
	}
	return acc
}

// The request and its reply: the asker writes e and wakes the counter,
// the counter adds it and wakes the asker with the new state.
var counter struct {
	e, s, n    int
	req, reply uintptr
}

func serve() {
	for {
		park(&counter.req)
		if counter.n == 0 {
			return
		}
		counter.s += counter.e
		unpark(&counter.reply)
	}
}

func ask(n int) int {
	counter.n = 1
	go serve()
	acc := 0
	for i := 0; i < n; i++ {
		counter.e = i & 3
		unpark(&counter.req)
		park(&counter.reply)
		acc = (acc + counter.s) & 0xffff
	}
	counter.n = 0
	unpark(&counter.req)
	return acc
}

func main() {
	if len(os.Args) != 4 || os.Args[1] != "bench" {
		fmt.Fprintln(os.Stderr, "usage: floor bench KIND N")
		os.Exit(2)
	}
	n, err := strconv.Atoi(os.Args[3])
	if err != nil {
		os.Exit(2)
	}
	switch os.Args[2] {
	case "spawn":
		fmt.Println(spawn(n))
	case "ask":
		fmt.Println(ask(n))
	default:
		os.Exit(2)
	}
}
