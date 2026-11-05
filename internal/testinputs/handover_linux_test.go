//go:build linux && (amd64 || arm64)

package testinputs

import (
	"fmt"
	"net"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

// cloneBeforeHandoverVariable makes the observed child call clone3 from
// another thread between installFilter and the handover of the listener,
// and wait for that call: glibc's pthread_create does so when the runtime
// starts a thread in that window.
const cloneBeforeHandoverVariable = "ERGO_TESTINPUTS_CLONE_BEFORE_HANDOVER"

const sysClone3 = 435 // one number on every architecture

var _ = func() bool {
	if os.Getenv(cloneBeforeHandoverVariable) != "" {
		beforeHandover = cloneOnAnotherThread
	}
	return true
}()

// cloneOnAnotherThread calls clone3 with no arguments, which the kernel
// refuses after the filter, from a thread other than the locked one, and
// waits for it. First it makes the runtime start threads from the locked
// one: each go statement wakes a thread for an idle P, and the earlier
// goroutines hold theirs in a blocking read. With cgo (package net links
// it), the runtime starts a thread with pthread_create, and glibc with
// clone3.
func cloneOnAnotherThread() {
	var pipe [2]int
	if err := syscall.Pipe(pipe[:]); err != nil {
		panic(err)
	}
	blocked := make(chan struct{})
	for range blockedThreads {
		go func() {
			blocked <- struct{}{}
			syscall.Read(pipe[0], make([]byte, 1))
		}()
		<-blocked
	}
	done := make(chan syscall.Errno)
	go func() {
		_, _, errno := syscall.RawSyscall(sysClone3, 0, 0, 0)
		done <- errno
	}()
	fmt.Fprintf(os.Stderr, "clone3 before the handover: %v\n", <-done)
	syscall.Close(pipe[1]) // the reads end
}

// blockedThreads is how many threads the child holds in a read before the
// handover.
const blockedThreads = 32

// net links runtime/cgo, so the child starts threads as a cgo test process
// does.
var _ = net.IPv4len

// A call that another thread makes before the observer holds the listener
// must not wait for the observer, since the handover waits for it.
func TestCallBeforeTheHandoverNeverWaits(t *testing.T) {
	if observed {
		return // the observed child: its init did the handover
	}
	output, err := os.CreateTemp(t.TempDir(), "child")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(cloneBeforeHandoverVariable, "1")
	args, stdout, stderr := os.Args, os.Stdout, os.Stderr
	os.Args = []string{os.Args[0], "-test.run=^TestCallBeforeTheHandoverNeverWaits$"}
	os.Stdout, os.Stderr = output, output
	type result struct {
		code int
		rec  *recorder
		err  error
	}
	results := make(chan result, 1)
	go func() {
		code, rec, err := observe(t.TempDir())
		results <- result{code, rec, err}
	}()
	var r result
	select {
	case r = <-results:
	case <-time.After(time.Minute):
		// The child dies with the observing thread (Pdeathsig) when this
		// test binary exits.
		r.err = fmt.Errorf("the observed child did not end within a minute")
	}
	os.Args, os.Stdout, os.Stderr = args, stdout, stderr
	text, _ := os.ReadFile(output.Name())
	if r.err != nil {
		t.Fatalf("%v; child output:\n%s", r.err, text)
	}
	if reason := r.rec.reason(); reason != "" || r.code != 0 {
		t.Fatalf("exit %d, refused: %q; child output:\n%s", r.code, reason, text)
	}
	if !strings.Contains(string(text), "clone3 before the handover: invalid argument") {
		t.Fatalf("the child made no clone3 call before the handover; child output:\n%s", text)
	}
}
