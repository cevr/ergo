package lower

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"

	"ergo.local/prototype/internal/lawledger"
)

// std sync (stdlib.md, Sync and STM 1): sync.Ref[T] is runtime/v1's
// *Ref[T], whose operations are its methods, and sync.Modify is
// v1.RefModify, whose function returns the Go form of the tuple (R, T).

// syncLawsProbe restates each law whose ledger runner is TestSyncLaws, as
// a test of the same name (TestStdLawsRun's naming). UpdateCallsOnce runs
// on Refs of int and of string; UpdateIsAtomic on four tasks. The
// Semaphore, Latch and Deferred laws (SB1) call the package's functions in
// the dot form, as a user does.
const syncLawsProbe = `import queue
import sync
import task
import time

test SyncRefUpdateCallsOnce(a int, s string) {
	let r = sync.NewRef[int](a)
	let mut calls = 0
	r.Update(func(v int) int {
		calls = calls + 1
		v
	})
	assert calls == 1
	let t = sync.NewRef[string](s)
	t.Update(func(v string) string {
		calls = calls + 1
		v + s
	})
	assert calls == 2
	assert t.Get() == s + s
}

test SyncGetAfterSet(a int, b int) {
	let r = sync.NewRef[int](a)
	assert r.Get() == a
	r.Set(b)
	assert r.Get() == b
	let copy = r
	copy.Set(a)
	assert r.Get() == a
}

test SyncUpdateComposes(a int) {
	let r = sync.NewRef[int](a)
	r.Update(func(v int) int { v + 1 })
	r.Update(func(v int) int { v * 3 })
	assert r.Get() == (a + 1) * 3
}

test SyncModifyReturnsAndStores(a int, b int) {
	let r = sync.NewRef[int](a)
	let old = sync.Modify(r, func(v int) (int, int) { (v, b) })
	assert old == a
	assert r.Get() == b
	let text = sync.Modify(r, func(v int) (string, int) { ("\(v)", v + 1) })
	assert text == "\(b)"
	assert r.Get() == b + 1
}

func bump(r sync.Ref[int], k int) {
	for _ in 0..k {
		r.Update(func(v int) int { v + 1 })
	}
}

test SyncUpdateIsAtomic(n int) {
	let k = 1000 + (n % 1000 + 1000) % 1000
	let r = sync.NewRef[int](0)
	let a = task.Go() { bump(r, k) }
	let b = task.Go() { bump(r, k) }
	let c = task.Go() { bump(r, k) }
	let d = task.Go() { bump(r, k) }
	a.Wait()
	b.Wait()
	c.Wait()
	d.Wait()
	assert r.Get() == 4 * k
}

func crowd(s sync.Semaphore, inside sync.Ref[int], most sync.Ref[int], done sync.Ref[int]) {
	for _ in 0..50 {
		s.With() {
			let now = sync.Modify(inside, func(v int) (int, int) { (v + 1, v + 1) })
			most.Update(func(m int) int { if now > m { now } else { m } })
			let q = queue.New[bool](1)
			q.Push(true) catch {
			case queue.Closed:
			}
			let _ = q.Poll()
			inside.Update(func(v int) int { v - 1 })
			done.Update(func(v int) int { v + 1 })
		}
	}
}

test SyncNeverMoreThanN(n int) {
	let size = 1 + (n % 3 + 3) % 3
	let s = sync.NewSemaphore(size)
	let inside = sync.NewRef[int](0)
	let most = sync.NewRef[int](0)
	let done = sync.NewRef[int](0)
	let a = task.Go() { crowd(s, inside, most, done) }
	let b = task.Go() { crowd(s, inside, most, done) }
	let c = task.Go() { crowd(s, inside, most, done) }
	let d = task.Go() { crowd(s, inside, most, done) }
	a.Wait()
	b.Wait()
	c.Wait()
	d.Wait()
	assert most.Get() <= size
	assert done.Get() == 4 * 50
	assert s.Available() == size
}

test SyncReleaseOnFailure(n int) {
	let s = sync.NewSemaphore(1 + (n % 3 + 3) % 3)
	let failed = s.With(func() bool fails queue.Closed { queue.Closed }) catch {
	case queue.Closed:
		true
	}
	assert failed
	assert s.Available() == s.Size()
}

test SyncReleaseOnInterrupt() {
	let s = sync.NewSemaphore(1)
	let m = time.NewManual(0)
	let holder = with context { Scheduler: m } {
		task.Go() {
			s.With() {
				Scheduler.Sleep(1h)
			}
		}
	}
	for m.Sleepers() == 0 {
	}
	assert s.Available() == 0
	let waiter = task.Go() {
		s.With() {
		}
	}
	waiter.Stop()
	assert s.Available() == 0
	holder.Stop()
	assert s.Available() == 1
}

test SyncLatchOpensOnce(k int) {
	let l = sync.NewLatch()
	assert !l.IsOpen()
	assert l.Open()
	for _ in 0..(k % 3 + 3) % 3 + 1 {
		assert !l.Open()
	}
	l.Await()
	assert l.IsOpen()
}

test SyncOpenReleasesWaiters(n int) {
	let l = sync.NewLatch()
	let copy = l
	let a = task.Go() {
		l.Await()
		l.IsOpen()
	}
	let b = task.Go() {
		copy.Await()
		copy.IsOpen()
	}
	let opened = if n % 2 == 0 { l.Open() } else { copy.Open() }
	assert opened
	assert a.Wait()
	assert b.Wait()
}

test SyncDeferredSetsOnce(a int, b int) {
	let d = sync.NewDeferred[int]()
	assert d.Peek() == None
	assert d.Complete(a)
	assert !d.Complete(b)
	assert d.Wait() == a
	assert d.Peek() == Some[int]{Value: a}
}

test SyncWaitSeesTheValue(a int) {
	let d = sync.NewDeferred[int]()
	let w = task.Go() { d.Wait() }
	let p = task.Go() { d.Complete(a) }
	assert p.Wait()
	assert w.Wait() == a
}
`

// syncLawStatements is the statement of each law the probe restates, the
// law's source text as the ledger writes it, by ledger key.
var syncLawStatements = map[string]string{
	"sync GetAfterSet":            `GetAfterSet(a int, b int) { let r = NewRef[int](a) let first = r.Get() == a r.Set(b) first && r.Get() == b }`,
	"sync ModifyReturnsAndStores": `ModifyReturnsAndStores(a int, b int) { let r = NewRef[int](a) let old = Modify(r, func(v int) (int, int) { (v, b) }) old == a && r.Get() == b }`,
	"sync Ref.UpdateCallsOnce":    `UpdateCallsOnce() { let mut calls = 0 Update(func(v T) T { calls = calls + 1 v }) calls == 1 }`,
	"sync UpdateComposes":         `UpdateComposes(a int) { let r = NewRef[int](a) r.Update(func(v int) int { v + 1 }) r.Update(func(v int) int { v * 3 }) r.Get() == (a+1)*3 }`,
	"sync UpdateIsAtomic":         `UpdateIsAtomic(n int) { let k = 1000 + (n%1000+1000)%1000 let r = NewRef[int](0) let a = task.Go() { for _ in 0..k { r.Update(func(v int) int { v + 1 }) } } let b = task.Go() { for _ in 0..k { r.Update(func(v int) int { v + 1 }) } } a.Wait() b.Wait() r.Get() == 2*k }`,
	"sync NeverMoreThanN":         `NeverMoreThanN(n int) { let size = 1 + (n%3+3)%3 let s = NewSemaphore(size) let inside = NewRef[int](0) let most = NewRef[int](0) let done = NewRef[int](0) let a = task.Go() { crowd(s, inside, most, done) } let b = task.Go() { crowd(s, inside, most, done) } let c = task.Go() { crowd(s, inside, most, done) } let d = task.Go() { crowd(s, inside, most, done) } a.Wait() b.Wait() c.Wait() d.Wait() most.Get() <= size && done.Get() == 4*50 && Available(s) == size }`,
	"sync ReleaseOnFailure":       `ReleaseOnFailure(n int) { let s = NewSemaphore(1 + (n%3+3)%3) let failed = With(s, func() bool fails queue.Closed { queue.Closed }) catch { case queue.Closed: true } failed && Available(s) == Size(s) }`,
	"sync ReleaseOnInterrupt":     `ReleaseOnInterrupt() { let s = NewSemaphore(1) let m = time.NewManual(0) let holder = with context { Scheduler: m } { task.Go() { With(s) { Scheduler.Sleep(1h) } } } for m.Sleepers() == 0 { } let taken = Available(s) == 0 let waiter = task.Go() { With(s) { } } waiter.Stop() let none = Available(s) == 0 holder.Stop() taken && none && Available(s) == 1 }`,
	"sync LatchOpensOnce":         `LatchOpensOnce(k int) { let l = NewLatch() let closed = !IsOpen(l) let first = Open(l) let mut later = true for _ in 0..(k%3+3)%3+1 { later = later && !Open(l) } Await(l) closed && first && later && IsOpen(l) }`,
	"sync OpenReleasesWaiters":    `OpenReleasesWaiters(n int) { let l = NewLatch() let copy = l let a = task.Go() { Await(l) IsOpen(l) } let b = task.Go() { Await(copy) IsOpen(copy) } let opened = if n%2 == 0 { Open(l) } else { Open(copy) } opened && a.Wait() && b.Wait() }`,
	"sync DeferredSetsOnce":       `DeferredSetsOnce(a int, b int) { let d = NewDeferred[int]() let empty = Peek(d) == None let first = Complete(d, a) let second = Complete(d, b) empty && first && !second && Wait(d) == a && Peek(d) == Some[int]{Value: a} }`,
	"sync WaitSeesTheValue":       `WaitSeesTheValue(a int) { let d = NewDeferred[int]() let w = task.Go() { Wait(d) } let p = task.Go() { Complete(d, a) } p.Wait() && w.Wait() == a }`,
}

// TestSyncLaws runs the probe with --seed 1; lawledger.Run holds that it
// restates exactly the ledger's laws whose runner is this test, each with
// the row's statement, and that each passed.
func TestSyncLaws(t *testing.T) {
	needsBuild(t)
	if laws := lawProbe(t, "synclaws", syncLawsProbe, syncLawStatements); laws != nil {
		lawledger.Run(t, laws)
	}
}

// recorderProbe holds that io.Recorder, now kept in a sync.Ref, behaves as
// it did over its Map: writes to Stdout and Stderr through a context, and
// through a copy, are kept in order, line feeds included, and IsTerminal
// is false. RecorderTasks writes from four tasks at once, which was a data
// race over the Map: every write is kept whole.
const recorderProbe = `import io
import strings
import task

func both(n int) needs io.Stdout | io.Stderr {
	io.Stdout.Print("o\(n)")
	io.Stderr.Eprintln("e")
	io.Stdout.Println("l")
	io.Stderr.Eprint("f")
}

test RecorderKeepsOrder(n int, s string) {
	let r = io.NewRecorder()
	assert r.Text() == ""
	with context { io.Stdout: r, io.Stderr: r } { both(n) }
	assert r.Text() == "o\(n)e\nl\nf"
	let copy = r
	copy.Print(s)
	assert r.Text() == "o\(n)e\nl\nf" + s
	assert !r.IsTerminal()
}

func write(r io.Recorder, k int) {
	for _ in 0..k {
		r.Print("ab")
	}
}

test RecorderTasks(n int) {
	let k = 200 + (n % 200 + 200) % 200
	let r = io.NewRecorder()
	let a = task.Go() { write(r, k) }
	let b = task.Go() { write(r, k) }
	let c = task.Go() { write(r, k) }
	let d = task.Go() { write(r, k) }
	a.Wait()
	b.Wait()
	c.Wait()
	d.Wait()
	assert r.Text() == strings.Repeat("ab", 4 * k)
}
`

// TestIORecorderBehavesAsBefore runs recorderProbe with --seed 1, on four
// processors, so the tasks of RecorderTasks run at once whatever the test
// process's GOMAXPROCS. RecorderKeepsOrder passes at the base too (the Map
// Recorder); RecorderTasks ends the base's binary with "fatal error:
// concurrent map writes" (measured at fd156fbb with GOMAXPROCS=2).
//
// It builds Go because it checks what the built program prints and how it
// exits; no in-process check observes that, and Ergo has no reference
// interpreter yet (test-speed T8).
func TestIORecorderBehavesAsBefore(t *testing.T) {
	needsBuild(t)
	exes := tableExes(t, "recorder")
	if exes == nil {
		return
	}
	cmd := exec.Command(exes["recorder"], "--seed", "1")
	cmd.Env = append(os.Environ(), "GOMAXPROCS=4")
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	code := 0
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatal(err)
		}
		code = exit.ExitCode()
	}
	stdout, stderr := out.String(), errOut.String()
	if code != 0 {
		t.Errorf("exit %d\n%s\n%s", code, stdout, firstLines(stderr, 20))
	}
	for _, name := range []string{"RecorderKeepsOrder", "RecorderTasks"} {
		if !strings.Contains(stdout, name) {
			t.Errorf("the probe did not report %s:\n%s", name, stdout)
		}
	}
}
