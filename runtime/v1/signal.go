//go:build linux || darwin || dragonfly || freebsd || netbsd || openbsd

package v1

import (
	"os"
	"os/signal"
	"sync"
	"syscall"
)

// The bodies of std signal's host provider (signal.Watch and Unwatch).
// A code names a stop: 1 SIGINT, 2 SIGTERM, 3 SIGHUP, 4 SIGUSR1,
// 5 SIGUSR2 (std signal's codeOf).

// signalWatches holds each Watch's channel by the number SignalWatch gave.
var signalWatches = struct {
	sync.Mutex
	next int
	held map[int]chan os.Signal
}{held: map[int]chan os.Signal{}}

// signalOfCode is the host signal of a stop code.
func signalOfCode(c int) os.Signal {
	switch c {
	case 1:
		return syscall.SIGINT
	case 2:
		return syscall.SIGTERM
	case 3:
		return syscall.SIGHUP
	case 4:
		return syscall.SIGUSR1
	default:
		return syscall.SIGUSR2
	}
}

// codeOfSignal is the stop code of a host signal signalOfCode gave.
func codeOfSignal(s os.Signal) int {
	switch s {
	case syscall.SIGINT:
		return 1
	case syscall.SIGTERM:
		return 2
	case syscall.SIGHUP:
		return 3
	case syscall.SIGUSR1:
		return 4
	default:
		return 5
	}
}

// SignalNumber is the host's number of the signal of a stop code: the
// reference of std signal's StatusIsTheShells law.
//
//ergo:host
//ergo:wait none
func SignalNumber(code int) int {
	return int(signalOfCode(code).(syscall.Signal))
}

// SignalWatch is the body of signal.Watch: from now until SignalUnwatch
// of the number it returns, each host signal of kinds is offered to q as
// its code instead of taking its default action. A stop that finds q full
// is dropped, as the host coalesces pending signals of one kind. Offer
// never waits, so the forwarding goroutine never blocks on a task.
//
//ergo:host
//ergo:wait none
func SignalWatch(q *OpenQueue[int], kinds []int) int {
	ch := make(chan os.Signal, 8)
	sigs := make([]os.Signal, 0, len(kinds))
	for _, k := range kinds {
		sigs = append(sigs, signalOfCode(k))
	}
	if len(sigs) > 0 {
		signal.Notify(ch, sigs...)
	}
	signalWatches.Lock()
	signalWatches.next++
	n := signalWatches.next
	signalWatches.held[n] = ch
	signalWatches.Unlock()
	go func() {
		for s := range ch {
			_ = q.Offer(codeOfSignal(s))
		}
	}()
	return n
}

// SignalUnwatch is the body of signal.Unwatch: the kinds watch n took go
// back to Go's os/signal default action (ending the process for SIGINT,
// SIGTERM, and SIGHUP; taking no action for SIGUSR1 and SIGUSR2), unless
// another watch still takes them, and its forwarding goroutine ends. A
// second call of n does nothing.
//
//ergo:host
//ergo:wait none
func SignalUnwatch(n int) {
	signalWatches.Lock()
	ch, held := signalWatches.held[n]
	delete(signalWatches.held, n)
	signalWatches.Unlock()
	if held {
		signal.Stop(ch)
		close(ch)
	}
}
