package v1

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"ergo.local/prototype/internal/lawledger"
	"ergo.local/prototype/runtime/rt"
)

// The stop protocol of ExecRun (exec.go) as a model. A case is a program,
// generated from a seed, and a caller:
//
//   - the program writes lines to stdout and stderr, may start grandchildren
//     in its process group (one that keeps the program's output open, one
//     that closes it), writes each pid it starts to a file of its own, then
//     either exits with a status, ends by a signal, or blocks;
//   - the caller is stopped before the call, at a random point while the
//     program runs, inside a mask, or never.
//
// The model's states are Running, Exited(status) and Killed. A program that
// blocks is Running until the stop, which makes it Killed: Run raises
// rt.Interrupted. A program that ends is Exited(status): Run returns the
// status and all the output, also when the stop comes inside a mask. A
// caller stopped before the call starts nothing. The invariants, after each
// call: the program itself is reaped (its pid names no process: Run waited
// for it, so no zombie of it is left either), and no process of its group
// is left (each pid in its file names no process, not even a zombie: Run
// waited until the host reaped them). Each started program's group gets one
// kill, while the program is not yet reaped (execKillHook). The test sends
// no signal: it reads /proc, and
// only for pids its own programs wrote.

// execOutcome is the model's end state of one case.
type execOutcome struct {
	killed bool // Killed; otherwise Exited(status)
	status int
	stdout string
	stderr string
	none   bool // the caller was stopped before the call: nothing started
}

// execCase is one generated program and its caller.
type execCase struct {
	script  string
	pidfile string
	stop    string // "never", "before", "running", "masked"
	delay   time.Duration
	holder  bool // a grandchild keeps stdout open past the exit
	want    execOutcome
}

// genExecCase builds case i from r in dir.
func genExecCase(r *rand.Rand, dir string, i int) execCase {
	c := execCase{pidfile: filepath.Join(dir, "pids-"+strconv.Itoa(i))}
	var sb strings.Builder
	fmt.Fprintf(&sb, "echo $$ >> %s\n", c.pidfile)
	var out, errOut strings.Builder
	for j := range r.IntN(4) {
		line := strings.Repeat(string(rune('a'+j)), 1+r.IntN(3000))
		if r.IntN(2) == 0 {
			fmt.Fprintf(&sb, "echo %s\n", line)
			out.WriteString(line + "\n")
		} else {
			fmt.Fprintf(&sb, "echo %s >&2\n", line)
			errOut.WriteString(line + "\n")
		}
	}
	if r.IntN(2) == 0 {
		// A grandchild that closes the program's output: only the group
		// kill ends it.
		fmt.Fprintf(&sb, "sleep 20 </dev/null >/dev/null 2>&1 &\necho $! >> %s\n", c.pidfile)
	}
	switch r.IntN(4) {
	case 0:
		c.stop = "never"
	case 1:
		c.stop = "before"
	case 2:
		c.stop = "running"
	case 3:
		c.stop = "masked"
	}
	c.delay = time.Duration(r.IntN(40)) * time.Millisecond
	if c.stop != "before" && r.IntN(8) == 0 {
		// A grandchild that holds stdout open: Run returns WaitDelay
		// after the exit, with all the program wrote.
		c.holder = true
		fmt.Fprintf(&sb, "sleep 20 &\necho $! >> %s\n", c.pidfile)
	}
	end := r.IntN(3)
	if c.stop == "running" {
		// The program blocks, so the stop finds it Running.
		end = 3
	}
	if c.stop == "masked" {
		// Masked: the program runs to its end, which takes longer than the
		// stop's delay, so the stop lands while it runs.
		sb.WriteString("sleep 0.05\n")
	}
	switch end {
	case 0:
		status := r.IntN(5)
		fmt.Fprintf(&sb, "exit %d\n", status)
		c.want = execOutcome{status: status}
	case 1:
		sb.WriteString("kill -TERM $$\nsleep 5\n")
		c.want = execOutcome{status: 128 + 15}
	case 2:
		c.want = execOutcome{status: 0}
	case 3:
		sb.WriteString("sleep 20\necho never\n")
		c.want = execOutcome{killed: true}
	}
	c.want.stdout, c.want.stderr = out.String(), errOut.String()
	if c.stop == "before" {
		c.want = execOutcome{none: true}
	}
	c.script = sb.String()
	return c
}

// runExecCase runs c's program on a task of its own and stops the task as
// c says. It returns what the call gave: its result, its error, and
// whether it raised rt.Interrupted.
func runExecCase(t *testing.T, c execCase) (res ExecResult, err error, interrupted bool, took time.Duration) {
	t.Helper()
	task := rt.Root("exec test")
	if c.stop == "before" {
		task.Interrupt()
	}
	type ret struct {
		res         ExecResult
		err         error
		interrupted bool
	}
	started := make(chan struct{})
	done := make(chan ret, 1)
	begin := time.Now()
	go func() {
		var out ret
		defer func() {
			if p := recover(); p != nil {
				if p != rt.Interrupted {
					panic(p)
				}
				out.interrupted = true
			}
			done <- out
		}()
		if c.stop == "masked" {
			m := task.Mask()
			defer m.Unmask()
		}
		close(started)
		out.res, out.err = ExecRun(task, "sh", []string{"-c", c.script}, nil, "", "")
	}()
	<-started
	if c.stop == "running" || c.stop == "masked" {
		// The stop lands while the program runs: after it wrote its pid,
		// at a random point of what follows.
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
			if data, _ := os.ReadFile(c.pidfile); len(data) > 0 {
				break
			}
		}
		time.Sleep(c.delay)
		task.Interrupt()
	}
	select {
	case got := <-done:
		return got.res, got.err, got.interrupted, time.Since(begin)
	case <-time.After(15 * time.Second):
		t.Fatalf("Run did not return in 15s for %s:\n%s", c.stop, c.script)
		return
	}
}

// procLine is the stat line of pid, for a failure message.
func procLine(pid int) string {
	data, _ := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	return strings.TrimSpace(string(data))
}

// procExists reports whether pid names a process of the group pgid: one
// with an entry in /proc, a zombie or a process the kernel is ending
// included. An entry that gives no text (the kernel is tearing the
// process down) exists: the round-1 test read it as gone, and then saw
// the same process run 22µs later. The generated programs never leave
// their group, so a pid in another group is a new process that the host
// gave a freed pid (this host's pids wrap at 4194304), not one of ours.
func procExists(pid, pgid int) bool {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if errors.Is(err, fs.ErrNotExist) {
		return false
	}
	// After the parenthesized command name: state, ppid, pgrp.
	s := string(data)
	i := strings.LastIndexByte(s, ')')
	if err != nil || i < 0 {
		return true
	}
	f := strings.Fields(s[i+1:])
	return len(f) < 3 || f[2] == strconv.Itoa(pgid)
}

// readPids is the pids a program wrote to its pid file, the program's own
// first.
func readPids(t *testing.T, path string) []int {
	t.Helper()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var pids []int
	for _, f := range strings.Fields(string(data)) {
		pid, err := strconv.Atoi(f)
		if err != nil {
			t.Fatalf("pid file %s: %q", path, data)
		}
		pids = append(pids, pid)
	}
	return pids
}

// TestExecStopModel runs 120 generated cases against the model above.
func TestExecStopModel(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	if _, err := os.Stat("/proc/self/stat"); err != nil {
		t.Skip("no /proc")
	}
	dir := t.TempDir()
	// The kill comes before the reap: at each kill the program (the
	// group's leader) still has its /proc entry in its own group, a
	// process or a zombie, so the group's id is still the body's.
	var mu sync.Mutex
	var kills []string
	execKillHook = func(group int) {
		mu.Lock()
		defer mu.Unlock()
		if procExists(group, group) {
			kills = append(kills, "held")
		} else {
			kills = append(kills, "reaped: "+procLine(group))
		}
	}
	t.Cleanup(func() { execKillHook = nil })
	r := rand.New(rand.NewPCG(7, 11))
	seen := map[string]int{}
	for i := range 120 {
		c := genExecCase(r, dir, i)
		mu.Lock()
		kills = nil
		mu.Unlock()
		res, err, interrupted, took := runExecCase(t, c)
		mu.Lock()
		caseKills := kills
		mu.Unlock()
		state := c.stop
		if c.want.killed {
			state += "/killed"
		}
		if c.holder {
			state += "/holder"
		}
		seen[state]++
		where := fmt.Sprintf("case %d (%s):\n%s", i, state, c.script)
		pids := readPids(t, c.pidfile)
		switch {
		case c.want.none:
			if !interrupted || len(pids) != 0 {
				t.Errorf("%s\nstopped before the call: interrupted %v, pids %v; want an interruption and no process", where, interrupted, pids)
			}
			continue
		case c.want.killed:
			if !interrupted {
				t.Errorf("%s\nwant Killed (rt.Interrupted); got %+v, %v", where, res, err)
			}
		default:
			if interrupted || err != nil {
				t.Errorf("%s\nwant Exited(%d); got interrupted %v, err %v", where, c.want.status, interrupted, err)
				break
			}
			if res.Status != c.want.status {
				t.Errorf("%s\nstatus %d, want %d", where, res.Status, c.want.status)
			}
			if res.Stdout != c.want.stdout || res.Stderr != c.want.stderr {
				t.Errorf("%s\noutput incomplete: stdout %d bytes (want %d), stderr %d bytes (want %d)", where, len(res.Stdout), len(c.want.stdout), len(res.Stderr), len(c.want.stderr))
			}
			if c.holder && took < execWaitDelay {
				t.Errorf("%s\nreturned after %v, before WaitDelay %v, while a grandchild held stdout", where, took, execWaitDelay)
			}
		}
		if len(pids) == 0 {
			t.Errorf("%s\nthe program wrote no pid", where)
			continue
		}
		if procExists(pids[0], pids[0]) {
			t.Errorf("%s\nRun returned and the program %d is not reaped", where, pids[0])
		}
		if len(caseKills) != 1 || caseKills[0] != "held" {
			t.Errorf("%s\nkills of group %d: %q; want one, while the unreaped program held the group", where, pids[0], caseKills)
		}
		for _, pid := range pids[1:] {
			// Run waited until the group was empty (execAwaitGroupEnd), so the
			// check reads /proc once, at once, and a zombie fails it too.
			if procExists(pid, pids[0]) {
				t.Errorf("%s\nRun returned and the grandchild %d of group %d is left: %q", where, pid, pids[0], procLine(pid))
			}
		}
	}
	for _, state := range []string{"never", "before", "running/killed", "masked"} {
		if seen[state] == 0 {
			t.Errorf("no case of state %s: %v", state, seen)
		}
	}
	t.Logf("cases by state: %v", seen)
}

// TestExecStopEndsAPipeline is the body's side of the Effect case
// effect.child-process.kills-pipeline, whose source-level evidence is the
// subtest of that name in lower TestExecRuns.
func TestExecStopEndsAPipeline(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	if _, err := os.Stat("/proc/self/stat"); err != nil {
		t.Skip("no /proc")
	}
	// Effect: NodeChildProcessSpawner.test.ts "kills every process in a
	// pipeline". The program runs a pipeline of two stages, each with a
	// process of its own; a stop of the caller ends every one of them.
	t.Run("a stop ends every stage", func(t *testing.T) {
		// The checks are on the order of events, not on time: the stop
		// lands after the program wrote all three pids, and the program
		// writes "late" only when its stages end on their own, after their
		// sleep. Run waits for the group to end before it returns, so a
		// stop that does not end the program lets "late" reach the file
		// first. A slow host (a late shell start, a loaded CPU) changes
		// neither order; the test binary's -timeout bounds a hang.
		dir := t.TempDir()
		pidfile, latefile := filepath.Join(dir, "pids"), filepath.Join(dir, "late")
		script := fmt.Sprintf(`(sleep 20 & echo $! >> %[1]s; wait) | (sleep 20 & echo $! >> %[1]s; wait) &
while [ "$(wc -l < %[1]s)" -lt 2 ]; do sleep 0.01; done
echo $$ >> %[1]s
wait
echo late >> %[2]s
`, pidfile, latefile)
		task := rt.Root("exec test")
		done := make(chan bool, 1)
		go func() {
			defer func() { done <- recover() == rt.Interrupted }()
			_, _ = ExecRun(task, "sh", []string{"-c", script}, nil, "", "")
		}()
		var pids []int
		for len(pids) < 3 {
			select {
			case <-done:
				t.Fatalf("Run returned before the stop, with pids %v", readPids(t, pidfile))
			default:
			}
			time.Sleep(time.Millisecond)
			pids = readPids(t, pidfile)
		}
		task.Interrupt()
		if interrupted := <-done; !interrupted {
			t.Fatal("the stop did not end the call with rt.Interrupted")
		}
		if late, _ := os.ReadFile(latefile); len(late) > 0 {
			t.Fatalf("the pipeline ran to its end (late holds %q): the stop did not end the program", late)
		}
		if len(pids) != 3 {
			t.Fatalf("pids %v; want the two stages' and the program's", pids)
		}
		group := pids[2]
		for _, pid := range pids {
			if procExists(pid, group) {
				t.Errorf("Run returned and process %d of the pipeline is left: %q", pid, procLine(pid))
			}
		}
	})
}

// TestExecFailures maps each way a program fails to start to its failure,
// and holds the rest of the call's contract: the environment over the
// host's, the directory, the standard input and a status that is not 0.
func TestExecFailures(t *testing.T) {
	task := rt.Root("exec test")
	dir := t.TempDir()
	plain := filepath.Join(dir, "plain")
	if err := os.WriteFile(plain, []byte("echo x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		dir  string
		want error
	}{
		{"ergo-exec-test-missing-7f3a", "", ExecNotFound{Name: "ergo-exec-test-missing-7f3a"}},
		{"/nonexistent/loop-probe-x", "", ExecNotFound{Name: "/nonexistent/loop-probe-x"}},
		{"", "", ExecNotFound{Name: ""}},
		{plain, "", ExecDenied{Name: plain}},
		{dir, "", ExecDenied{Name: dir}}, // Go's LookPath: a directory is not executable
		{"sh", "", nil},
		{"true", filepath.Join(dir, "nowhere"), ExecFailed{Name: "true", Reason: "dir " + filepath.Join(dir, "nowhere") + ": no such file or directory"}},
		{"true", plain, ExecFailed{Name: "true", Reason: "dir " + plain + ": not a directory"}},
	} {
		_, err := ExecRun(task, c.name, nil, nil, c.dir, "")
		if err != c.want {
			t.Errorf("ExecRun(%q, dir %q) failed %#v, want %#v", c.name, c.dir, err, c.want)
		}
	}
	t.Setenv("ERGO_EXEC_HOST", "host")
	t.Setenv("ERGO_EXEC_OVER", "host")
	res, err := ExecRun(task, "sh", []string{"-c", `printf '%s %s %s|' "$ERGO_EXEC_HOST" "$ERGO_EXEC_OVER" "$(pwd)"; cat; echo oops >&2; exit 7`},
		map[string]string{"ERGO_EXEC_OVER": "set"}, dir, "in\nput")
	want := ExecResult{Status: 7, Stdout: "host set " + dir + "|in\nput", Stderr: "oops\n"}
	if err != nil || res != want {
		t.Errorf("ExecRun = %#v, %v; want %#v", res, err, want)
	}
	if e := (ExecFailed{Name: "a", Reason: "b"}).Error(); e != "exec.Failed{Name:a Reason:b}" {
		t.Errorf("Failed text %q", e)
	}
}

// TestExecNotFoundNamesTheProgram runs the law Runner.NotFoundNamesTheProgram
// against the host body: for each generated name, a NotFound failure names
// it. The names are of no program: a prefix that no host program has, a
// path under /nonexistent, the empty name and runs of dots and separators,
// so the test starts nothing it did not mean to.
func TestExecNotFoundNamesTheProgram(t *testing.T) {
	task := rt.Root("exec test")
	r := rand.New(rand.NewPCG(3, 5))
	names := []string{"", ".", "..", "/nonexistent/loop-probe-x", "ergo-law-missing"}
	alphabet := []string{"a", "Z", "_", "/", ".", "-", " ", "é", "\xff", "0"}
	for range 300 {
		var b strings.Builder
		for range r.IntN(6) {
			b.WriteString(alphabet[r.IntN(len(alphabet))])
		}
		if r.IntN(2) == 0 {
			// A name with no separator is looked up on the PATH only. With
			// one, it is a path relative to the working directory, the
			// package's own directory, which the test must not read
			// (closure sweep, batch-10 gate 1).
			names = append(names, "ergo-law-missing-"+strings.ReplaceAll(b.String(), "/", "_"))
		} else {
			names = append(names, "/nonexistent/loop-probe-"+b.String())
		}
	}
	lawledger.Run(t, map[string]lawledger.Law{
		"exec Runner.NotFoundNamesTheProgram": {Statement: `NotFoundNamesTheProgram(name string) { Run(New(name, []string{})) catch { case NotFound as n: return n.Name == name case Denied, Failed: return true } true }`, Check: func(t *testing.T) {
			notFound := 0
			for _, name := range names {
				_, err := ExecRun(task, name, nil, nil, "", "")
				var nf ExecNotFound
				if errors.As(err, &nf) {
					notFound++
					if nf.Name != name {
						t.Errorf("NotFoundNamesTheProgram(%q): the failure names %q", name, nf.Name)
					}
				}
			}
			if notFound < len(names)-3 {
				t.Errorf("only %d of %d missing names failed NotFound", notFound, len(names))
			}
		}},
	})
}

// BenchmarkExecRunTrue is Run("true") against its Go control,
// exec.CommandContext(ctx, "true") with stdout and stderr read into
// buffers and made strings, the same work: process start dominates both.
func BenchmarkExecRunTrue(b *testing.B) {
	b.Run("ergo", func(b *testing.B) {
		task := rt.Root("exec bench")
		b.ReportAllocs()
		for b.Loop() {
			if _, err := ExecRun(task, "true", nil, nil, "", ""); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("go", func(b *testing.B) {
		ctx := context.Background()
		b.ReportAllocs()
		for b.Loop() {
			var out, errOut bytes.Buffer
			cmd := exec.CommandContext(ctx, "true")
			cmd.Stdout, cmd.Stderr = &out, &errOut
			if err := cmd.Run(); err != nil {
				b.Fatal(err)
			}
			sinkString = out.String() + errOut.String()
		}
	})
}

// BenchmarkExecRunMiB reads 1 MiB of stdout, against the Go control that
// reads it into a bytes.Buffer and makes a string, as a caller that needs
// text does.
func BenchmarkExecRunMiB(b *testing.B) {
	args := []string{"-c", "1048576", "/dev/zero"}
	b.Run("ergo", func(b *testing.B) {
		task := rt.Root("exec bench")
		b.ReportAllocs()
		for b.Loop() {
			res, err := ExecRun(task, "head", args, nil, "", "")
			if err != nil || len(res.Stdout) != 1<<20 {
				b.Fatal(err, len(res.Stdout))
			}
		}
	})
	b.Run("go", func(b *testing.B) {
		ctx := context.Background()
		b.ReportAllocs()
		for b.Loop() {
			var out, errOut bytes.Buffer
			cmd := exec.CommandContext(ctx, "head", args...)
			cmd.Stdout, cmd.Stderr = &out, &errOut
			if err := cmd.Run(); err != nil {
				b.Fatal(err)
			}
			if s := out.String(); len(s) != 1<<20 {
				b.Fatal(len(s))
			}
		}
	})
}
