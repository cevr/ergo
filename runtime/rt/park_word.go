//go:build !race && !ergo_stockpark

package rt

import (
	"sync/atomic"
	"unsafe"
)

// parker is the word body: the park word is one uintptr that holds
// parkEmpty, parkNotified or the parked goroutine's g.
type parker = wordParker

type wordParker struct{ w uintptr }

// The Go runtime's park and ready. Their signatures are kept for the
// packages that link them (go.dev/issue/67401: "Do not remove or change
// the type signature"); a g is passed as its address, as gVisor does: g
// structs live in allgs and are never freed. TestParkWordLinksTheRuntime
// checks these declarations and the two reason numbers against the
// pinned release's source.
//
//go:linkname gopark runtime.gopark
func gopark(unlockf func(gp uintptr, lock unsafe.Pointer) bool, lock unsafe.Pointer, reason uint8, traceReason uint8, traceskip int)

//go:linkname goready runtime.goready
func goready(gp uintptr, traceskip int)

// The park's reasons: waitReasonSyncCondWait ("sync.Cond.Wait" in a
// goroutine dump, a sync wait for the leak profile) and
// traceBlockCondWait.
const (
	parkWaitReason  = 21
	parkTraceReason = 4
)

func (p *wordParker) park() {
	if atomic.CompareAndSwapUintptr(&p.w, parkNotified, parkEmpty) {
		return
	}
	gopark(parkCommit, unsafe.Pointer(&p.w), parkWaitReason, parkTraceReason, 1)
	// parkCommit leaves another goroutine's g in place and returns at once:
	// the misuse fails here. A woken waiter finds the word empty or
	// notified (unpark takes the g out before it readies it).
	if parkHeld(atomic.LoadUintptr(&p.w)) {
		panic(parkTwice)
	}
}

// parkHeld reports whether a word holds a parked goroutine's g.
func parkHeld(w uintptr) bool { return w != parkEmpty && w != parkNotified }

// parkCommit runs on the system stack once the waiter is off its M: it
// publishes the waiter's g in the empty word, so a waker that sees it
// readies the waiter. When a wake came since park looked, it takes the
// wake and the waiter runs on.
//
//go:nosplit
func parkCommit(gp uintptr, w unsafe.Pointer) bool {
	if atomic.CompareAndSwapUintptr((*uintptr)(w), parkEmpty, gp) {
		return true
	}
	// A wake came since park looked: take it. A word that holds another
	// goroutine's g keeps it (park fails loudly after the return).
	atomic.CompareAndSwapUintptr((*uintptr)(w), parkNotified, parkEmpty)
	return false
}

func (p *wordParker) unpark() {
	for {
		old := atomic.LoadUintptr(&p.w)
		switch old {
		case parkNotified:
			return
		case parkEmpty:
			if atomic.CompareAndSwapUintptr(&p.w, parkEmpty, parkNotified) {
				return
			}
		default:
			if atomic.CompareAndSwapUintptr(&p.w, old, parkEmpty) {
				goready(old, 1)
				return
			}
		}
	}
}

func (p *wordParker) parkedNow() bool {
	w := atomic.LoadUintptr(&p.w)
	return w != parkEmpty && w != parkNotified
}

func (p *wordParker) pending() bool { return atomic.LoadUintptr(&p.w) == parkNotified }
