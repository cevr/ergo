package bindc

import (
	"errors"
	"os/exec"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// TestLivePanicContainment (A6, ERGO_BINDC_LIVE=1) drives the panic
// containment of the generated fixture package
// (testdata/live/contain/main.go). On one locked thread: a panic in a
// call-scoped and in a registered callback completes every C frame (the
// fixture's started and returned counters are equal), the typed call that
// made the C call re-raises one *CallbackPanic with the typedef and the
// value, the invocations after the panic in that C call run no Go code and
// C gets the default (the fixture's sum of write results, and its count of
// unwritten out arrays); a panic in a set or a clear probe leaves the
// registry as C left it; an error value unwraps, panic(nil) is a
// *runtime.PanicNilError, a *CallbackPanic value is kept; a nested panic
// (callback, typed call, callback) surfaces at the outermost typed call
// with equal counters at both levels; and the next typed call is clean.
// Then 100 goroutines that do not lock their threads, with GOMAXPROCS at
// least 4 and runtime.Gosched in their callbacks, each re-raise only their
// own panic, while 32 other goroutines make typed calls with no callback
// (each takes the slow path of cbTake while panics are pending) and never
// raise one. In subprocesses, runtime.Goexit in a
// callback and in a function of Owner.Do ends the process with exit status
// 2 and a stderr line that names the typedef (or Owner.Do), before C
// frames unwind.
// stressTakers are the goroutines of the stress driver that make typed calls
// with no callback.
const stressTakers = 32

func TestLivePanicContainment(t *testing.T) {
	e := newLiveEnv(t)
	lib, driver := e.fixtureDriverFrom("testdata/live/contain/main.go")
	want := []string{
		"call-scoped panic: C frames completed, the typed call re-raised it",
		"no Go code ran after the panic, and C got the default",
		"registered panic: C frames completed, the typed call re-raised it, the registry is intact",
		"a panic in a set or a clear probe: the registry follows what C did",
		"the panic value: errors unwrap, panic(nil) is a PanicNilError, a CallbackPanic is kept",
		"nested panic surfaces at the outermost typed call; C counters match at both levels",
		"a panic the outer callback recovers leaves nothing pending",
		"the next typed call on the thread is clean",
	}
	out, err := e.try(e.root, "", nil, driver, "contain", lib)
	if got := okLines(out); err != nil || !slices.Equal(got, want) {
		t.Fatalf("the contain driver: %v\n%s\nwant the checks:\n%s", err, out, strings.Join(want, "\n"))
	}
	// 300 calls per goroutine with 32 takers: without the lock of
	// cbTakeHere, 20 of 20 runs failed on linux/amd64 (16 CPUs); without
	// the takers, 1 of 10 (bind-c-2 r3, measured).
	calls := max(300, *propCases/20)
	out, err = e.try(e.root, "", nil, driver, "stress", lib, strconv.Itoa(calls), strconv.Itoa(stressTakers))
	if got := okLines(out); err != nil || !slices.Equal(got, []string{"no goroutine got another goroutine's panic or lost its own"}) {
		t.Fatalf("the stress driver (%d calls per goroutine, %d takers): %v\n%s", calls, stressTakers, err, tail(out, 2000))
	}
	t.Log(strings.TrimSpace(out[:strings.Index(out, "\nok: ")]))
	for _, c := range []struct{ mode, line string }{
		{"goexit", "fixture: runtime.Goexit in the callback fx_write_callback would unwind C frames; exit status 2"},
		{"goexit-owner", "fixture: runtime.Goexit in a function of Owner.Do would stop the owner thread; exit status 2"},
	} {
		out, err := e.try(e.root, "", nil, driver, c.mode, lib)
		var xe *exec.ExitError
		if !errors.As(err, &xe) || xe.ExitCode() != 2 || !strings.Contains(out, c.line) || strings.Contains(out, "FAIL") {
			t.Fatalf("the %s driver: %v, want exit status 2 and the line %q, and no FAIL\n%s", c.mode, err, c.line, out)
		}
	}
}

// TestLiveOwnerModel (A6, ERGO_BINDC_LIVE=1, model-based, owner.qnt):
// max(100, bindc.cases/10) generated schedules of Do from up to 8
// goroutines, with jobs that use the fixture, panic, call Do again (inline)
// from the job and from a callback inside it, or call Close on the owner
// thread, and one Close at a generated point. Every Do and Close has a
// deadline, so a hang fails the driver. The oracle is the model's
// invariants: noStranded (every Do returns nil, the job's own panic, or
// ErrOwnerClosed for a job that never began), noStartAfterClose (no job
// begins after Close returned; Close returns after every begun job ended),
// noSelfWait (a Do on the owner thread runs inline); and every job runs on
// the owner thread (fx_thread_id), where no fixture call gets the
// wrong-thread status. On linux, a new thread that gets the ended owner
// thread's id (in a user and pid namespace) still gets ErrOwnerClosed from
// Do and Close.
func TestLiveOwnerModel(t *testing.T) {
	e := newLiveEnv(t)
	lib, driver := e.fixtureDriverFrom("testdata/live/contain/main.go")
	n := max(100, *propCases/10)
	want := []string{
		"noStranded: every Do returned nil, its panic, or ErrOwnerClosed",
		"noStartAfterClose: no job started after Close returned, and Close returned after the runner stopped",
		"noSelfWait: a Do on the owner thread ran inline",
		"every job ran on the owner thread, and no fixture call got the wrong-thread status",
	}
	out, err := e.try(e.root, "", nil, driver, "owner", lib, strconv.FormatUint(*propSeed, 10), strconv.Itoa(n))
	if got := okLines(out); err != nil || !slices.Equal(got, want) {
		t.Fatalf("the owner driver (seed %d, %d schedules): %v\n%s", *propSeed, n, err, tail(out, 3000))
	}
	t.Log(strings.TrimSpace(out[:strings.Index(out, "\nok: ")]))

	// Thread-id reuse (linux; darwin's 64-bit thread ids are not reused,
	// inferred): the model has no thread ids, so this state is driven
	// directly. In its own user and pid namespace the driver gives the
	// ended owner thread's id to a new thread through ns_last_pid; Do and
	// Close there must still return ErrOwnerClosed.
	if runtime.GOOS != "linux" {
		return
	}
	unshare, err := exec.LookPath("unshare")
	if err != nil {
		t.Fatalf("the thread-id reuse case needs unshare(1) on linux: %v", err)
	}
	out, err = e.try(e.root, "", nil, unshare, "--user", "--map-root-user", "--pid", "--fork", "--mount-proc", driver, "reuse", lib)
	if got := okLines(out); err != nil || !slices.Equal(got, []string{"after Close, a thread with the old owner id gets ErrOwnerClosed from Do and Close"}) {
		t.Fatalf("the reuse driver (in a user and pid namespace): %v\n%s", err, tail(out, 2000))
	}
	t.Log(strings.TrimSpace(out[:strings.Index(out, "\nok: ")]))
}
