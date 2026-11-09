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
// on Refs of int and of string; UpdateIsAtomic on four tasks.
const syncLawsProbe = `import sync
import task

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
`

// syncLawStatements is the statement of each law the probe restates, the
// law's source text as the ledger writes it, by ledger key.
var syncLawStatements = map[string]string{
	"sync GetAfterSet":            `GetAfterSet(a int, b int) { let r = NewRef[int](a) let first = r.Get() == a r.Set(b) first && r.Get() == b }`,
	"sync ModifyReturnsAndStores": `ModifyReturnsAndStores(a int, b int) { let r = NewRef[int](a) let old = Modify(r, func(v int) (int, int) { (v, b) }) old == a && r.Get() == b }`,
	"sync Ref.UpdateCallsOnce":    `UpdateCallsOnce() { let mut calls = 0 Update(func(v T) T { calls = calls + 1 v }) calls == 1 }`,
	"sync UpdateComposes":         `UpdateComposes(a int) { let r = NewRef[int](a) r.Update(func(v int) int { v + 1 }) r.Update(func(v int) int { v * 3 }) r.Get() == (a+1)*3 }`,
	"sync UpdateIsAtomic":         `UpdateIsAtomic(n int) { let k = 1000 + (n%1000+1000)%1000 let r = NewRef[int](0) let a = task.Go() { for _ in 0..k { r.Update(func(v int) int { v + 1 }) } } let b = task.Go() { for _ in 0..k { r.Update(func(v int) int { v + 1 }) } } a.Wait() b.Wait() r.Get() == 2*k }`,
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
func TestIORecorderBehavesAsBefore(t *testing.T) {
	needsBuild(t)
	exes := buildAll(t, []*lowered{lowerTestBuild(t, "recorder", recorderProbe)}, true)
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
