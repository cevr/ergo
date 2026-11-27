//go:build linux || darwin || dragonfly || freebsd || netbsd || openbsd

package stdlib_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"

	"ergo.local/prototype/internal/ergobuild"
)

const processChildWorkerSrc = `import fs
import io
import os
import process
import rpc
import signal
import strconv
import strings
import time

type WorkerReq { Arg string }
type WorkerAns { Out string }
type WorkerErr { Code int, Msg string }
type WorkerDefect { Msg string }

func blowStack(n int) int {
	if n < 0 {
		return 0
	}
	let a = n + 1
	let b = blowStack(a)
	a + b
}

func retainOOM(mb int) int {
	let chunk = strings.Repeat("X", 1 << 20)
	let mut held = []string{}
	for i in 0..mb {
		held = held.Append(chunk + strconv.Itoa(i))
	}
	held.Len()
}

func spinLoop(start time.Time) int needs os.Ambient | time.Clock {
	let w = signal.Watch([]signal.Stop{signal.Terminate})
	let mut acc = 0
	for {
		let _ = w.Poll()
		acc = acc + 1
		if acc % 1000 == 0 && time.Since(start) > 8s {
			return acc
		}
	}
	acc
}

func handle(tag string, payload string) string fails rpc.Failed needs os.Ambient | time.Clock defects {
	match tag {
	case "echo":
		let q = rpc.Decode[WorkerReq]("echo req", payload) catch {
		case rpc.Transport as tr:
			return rpc.Failed{Text: tr.Reason}
		}
		rpc.Encode(WorkerAns{Out: "echo:" + q.Arg})
	case "fail":
		return rpc.Failed{Text: rpc.Encode(WorkerErr{Code: 7, Msg: "boom"})}
	case "panic":
		die WorkerDefect{Msg: "worker fault"}
	case "limits":
		let text = fs.NewHost().ReadFile("/proc/self/limits") catch {
		case _:
			return rpc.Failed{Text: "cannot read limits"}
		}
		rpc.Encode(WorkerAns{Out: text})
	case "oom":
		let n = retainOOM(256)
		rpc.Encode(WorkerAns{Out: strconv.Itoa(n)})
	case "stack":
		let n = blowStack(1)
		rpc.Encode(WorkerAns{Out: strconv.Itoa(n)})
	case "spin":
		let started = time.NewHost().Now()
		let n = spinLoop(started)
		rpc.Encode(WorkerAns{Out: strconv.Itoa(n)})
	case _:
		return rpc.Failed{Text: "unknown rpc \(tag)"}
	}
}

func main() needs Ambient {
	let clk = time.NewHost()
	with context { time.Scheduler: time.NewLiveScheduler(), time.Clock: clk } {
		process.ServeWith(io.NewStdin(), io.NewHost(), func(tag string, payload string) string fails rpc.Failed needs os.Ambient | time.Clock defects {
			try handle(tag, payload)
		})
	}
}
`

const processChildDriverSrc = `import exec
import fs
import os
import process
import queue
import rpc
import strconv
import strings
import task
import time

type WorkerReq { Arg string }
type WorkerAns { Out string }
type WorkerErr { Code int, Msg string }

func fail(msg string) os.Exit { os.Exit{Code: 1, Message: msg} }

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i in 0..s.Len() {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	true
}

func lastRParen(s string) int {
	let mut idx = -1
	for i in 0..s.Len() {
		if s[i] == ')' {
			idx = i
		}
	}
	idx
}

func assertNoZombieChildren() fails os.Exit needs os.Ambient {
	let h = fs.NewHost()
	let myPid = strconv.Itoa(os.NewHost().Pid())
	let entries = h.ReadDir("/proc") catch {
	case _:
		return
	}
	for entry in entries {
		if !isDigits(entry) {
			continue
		}
		let stat = h.ReadFile("/proc/" + entry + "/stat") catch {
		case _:
			continue
		}
		let rp = lastRParen(stat)
		if rp < 0 || rp + 2 >= stat.Len() {
			continue
		}
		let tail = strings.TrimSpace(strings.Slice(stat, rp + 2, stat.Len()))
		let parts = strings.Split(tail, " ")
		if parts.Len() >= 2 && parts[1] == myPid && parts[0] == "Z" {
			return fail("left zombie child pid=\(entry) stat=\(stat)")
		}
	}
}

func timedChild(play own func(on time.Scheduler) process.Report) process.Report {
	let m = time.NewManual(0)
	let done = queue.New[int](1)
	with context { time.Scheduler: m } {
		let t = task.Go() {
			let r = play(m)
			done.Push(1) catch {
			case queue.Closed:
			}
			r
		}
		m.Advance(0ns)
		for done.Len() == 0 {
			m.Advance(1ms)
		}
		t.Wait()
	}
}

func reportsMatch(got process.Report, want process.Report) bool {
	if got.Cleaned != want.Cleaned {
		return false
	}
	match want.Ended {
	case process.Crashed as wc:
		match got.Ended {
		case process.Crashed as gc:
			if strings.HasPrefix(wc.Reason, "die at ") {
				if gc.Reason == wc.Reason {
					return true
				}
				return strings.HasPrefix(gc.Reason, "die at ") && strings.Contains(gc.Reason, "ScriptedFault) 0x") && strings.Contains(wc.Reason, "ScriptedFault) 0x")
			}
			strings.HasPrefix(gc.Reason, "die at ") && (strings.HasSuffix(gc.Reason, ": {" + wc.Reason + "}") || strings.HasSuffix(gc.Reason, "[" + wc.Reason + "]}") || strings.Contains(gc.Reason, "ScriptedFault) 0x"))
		case _:
			false
		}
	case _:
		got.Ended == want.Ended
	}
}

func runLaws(worker string) fails os.Exit needs os.Ambient | time.Scheduler {
	let c = process.Child(process.NewSpec(worker))
	for cleanups in 0..3 {
		let sGive = process.Script{Cleanups: cleanups, Waits: 0ns, Steps: 2, End: process.Gives{Value: 10 + cleanups}}
		let rGive = c.Play(process.Unlimited, sGive, time.NewManual(0))
		if !reportsMatch(rGive, process.Local.Play(process.Unlimited, sGive, time.NewManual(0))) {
			return fail("child Gives mismatch")
		}
		let sFail = process.Script{Cleanups: cleanups, Waits: 0ns, Steps: 1, End: process.Fails{Code: 5 + cleanups}}
		let rFail = c.Play(process.Unlimited, sFail, time.NewManual(0))
		if !reportsMatch(rFail, process.Local.Play(process.Unlimited, sFail, time.NewManual(0))) {
			return fail("child Fails mismatch")
		}
		let sPanic = process.Script{Cleanups: cleanups, Waits: 0ns, Steps: 0, End: process.Panics{Text: "fault\(cleanups)"}}
		let rPanic = c.Play(process.Unlimited, sPanic, time.NewManual(0))
		if !reportsMatch(rPanic, process.Local.Play(process.Unlimited, sPanic, time.NewManual(0))) {
			return fail("child Panics mismatch")
		}
		let limit = 2 + cleanups + 3
		let sOver = process.Script{Cleanups: cleanups, Waits: 0ns, Steps: 5, End: process.Gives{Value: 1}}
		let rOver = c.Play(process.Limits{..process.Unlimited, Steps: limit}, sOver, time.NewManual(0))
		if !reportsMatch(rOver, process.Report{Ended: process.OverSteps{Limit: limit}, Cleaned: cleanups}) {
			return fail("child OverSteps mismatch")
		}
	}
	for waitMs in 2..6 {
		let limitMs = 3
		let l = process.Limits{..process.Unlimited, Time: time.Times(1ms, limitMs)}
		let s = process.Script{Cleanups: 2, Waits: time.Times(1ms, waitMs), Steps: 1, End: process.Gives{Value: 77}}
		let got = timedChild(func(on time.Scheduler) process.Report {
			c.Play(l, s, on)
		})
		let want = timedChild(func(on time.Scheduler) process.Report {
			process.Local.Play(l, s, on)
		})
		if !reportsMatch(got, want) {
			return fail("child manual TimeIsExpired mismatch for wait=\(waitMs)")
		}
	}
	let liveRep = c.Play(
		process.Limits{..process.Unlimited, Time: 25ms},
		process.Script{Cleanups: 2, Waits: 250ms, Steps: 0, End: process.Gives{Value: 1}},
		time.NewLiveScheduler(),
	)
	if liveRep != (process.Report{Ended: process.OverTime{Limit: 25ms}, Cleaned: 2}) {
		return fail("child live OverTime mismatch")
	}
}

func runRPC(worker string) fails os.Exit needs os.Ambient | time.Scheduler {
	let c = process.Child(process.NewSpec(worker))
	let ans = process.Call[WorkerReq, WorkerAns](c, "echo", WorkerReq{Arg: "hello"}, process.Unlimited) catch {
	case _:
		return fail("echo RPC failed")
	}
	if ans.Out != "echo:hello" {
		return fail("unexpected echo output: \(ans.Out)")
	}
	let failAns = process.Call[WorkerReq, WorkerAns](c, "fail", WorkerReq{Arg: ""}, process.Unlimited) catch {
	case rpc.Failed as rf:
		if !strings.Contains(rf.Text, "boom") {
			return fail("unexpected rpc.Failed text: \(rf.Text)")
		}
		WorkerAns{Out: "caught-fail"}
	case _:
		return fail("expected rpc.Failed from fail op")
	}
	if failAns.Out != "caught-fail" {
		return fail("fail op unexpectedly succeeded: \(failAns.Out)")
	}
	let crashAns = process.Call[WorkerReq, WorkerAns](c, "panic", WorkerReq{Arg: ""}, process.Unlimited) catch {
	case process.Crashed as cr:
		if !strings.HasPrefix(cr.Reason, "die at ") || (!strings.Contains(cr.Reason, "worker fault") && !strings.Contains(cr.Reason, "WorkerDefect")) {
			return fail("unexpected panic crash reason: \(cr.Reason)")
		}
		WorkerAns{Out: "caught-panic"}
	case _:
		return fail("expected process.Crashed from panic op")
	}
	if crashAns.Out != "caught-panic" {
		return fail("panic op unexpectedly succeeded: \(crashAns.Out)")
	}
}

func runKill(worker string) fails os.Exit needs os.Ambient | time.Scheduler {
	for grace in []duration{0s, 30ms} {
		let c = process.Child(process.Spec{..process.NewSpec(worker), Grace: grace})
		let spinAns = process.Call[WorkerReq, WorkerAns](c, "spin", WorkerReq{Arg: "trap"}, process.Limits{..process.Unlimited, Time: 50ms}) catch {
		case process.OverTime as ot:
			if ot.Limit != 50ms {
				return fail("wrong OverTime limit (grace=\(grace))")
			}
			WorkerAns{Out: "caught-overtime"}
		case process.Crashed as cr:
			return fail("kill failed (grace=\(grace)): \(cr.Reason)")
		case _:
			return fail("unexpected error on spin (grace=\(grace))")
		}
		if spinAns.Out != "caught-overtime" {
			return fail("spin unexpectedly succeeded (grace=\(grace)): \(spinAns.Out)")
		}
	}
}

func runLimits(worker string) fails os.Exit needs os.Ambient | time.Scheduler {
	let c = process.Child(process.Spec{..process.NewSpec(worker), Limits: process.Limits{..process.Unlimited, Bytes: 96 << 20}})
	let ans = process.Call[WorkerReq, WorkerAns](c, "limits", WorkerReq{Arg: ""}, process.Unlimited) catch {
	case _:
		return fail("limits call failed")
	}
	let mut sawData = false
	for line in strings.Lines(ans.Out) {
		if strings.HasPrefix(line, "Max data size") {
			sawData = true
			if !strings.Contains(line, "100663296") {
				return fail("RLIMIT_DATA not applied inside child: \(line)")
			}
		}
	}
	if !sawData {
		return fail("missing Max data size in /proc/self/limits")
	}
}

func runOOM(worker string) fails os.Exit needs os.Ambient | time.Scheduler {
	let bounded = process.Child(process.Spec{..process.NewSpec(worker), Limits: process.Limits{..process.Unlimited, Bytes: 96 << 20}})
	let oomAns = process.Call[WorkerReq, WorkerAns](bounded, "oom", WorkerReq{Arg: ""}, process.Unlimited) catch {
	case process.Crashed:
		WorkerAns{Out: "caught-oom"}
	case _:
		return fail("expected process.Crashed when child exceeds Bytes limit")
	}
	if oomAns.Out != "caught-oom" {
		return fail("oom unexpectedly succeeded: \(oomAns.Out)")
	}
	let alive = process.Call[WorkerReq, WorkerAns](bounded, "echo", WorkerReq{Arg: "after-oom"}, process.Unlimited) catch {
	case _:
		return fail("parent failed to launch fresh child after OOM")
	}
	if alive.Out != "echo:after-oom" {
		return fail("unexpected output after OOM: \(alive.Out)")
	}
}

func runStack(worker string) fails os.Exit needs os.Ambient | time.Scheduler {
	let c = process.Child(process.NewSpec(worker))
	let stackAns = process.Call[WorkerReq, WorkerAns](c, "stack", WorkerReq{Arg: ""}, process.Unlimited) catch {
	case process.Crashed as cr:
		if !strings.Contains(cr.Reason, "stack overflow") {
			return fail("unexpected stack overflow reason: \(cr.Reason)")
		}
		WorkerAns{Out: "caught-stack"}
	case _:
		return fail("expected stack overflow Crashed")
	}
	if stackAns.Out != "caught-stack" {
		return fail("stack unexpectedly succeeded: \(stackAns.Out)")
	}
	let alive = process.Call[WorkerReq, WorkerAns](c, "echo", WorkerReq{Arg: "after-stack"}, process.Unlimited) catch {
	case _:
		return fail("parent failed to run child after stack overflow")
	}
	if alive.Out != "echo:after-stack" {
		return fail("unexpected output after stack overflow: \(alive.Out)")
	}
}

func runExec(worker string) fails os.Exit needs os.Ambient | time.Scheduler {
	let er = process.ChildWithExec(process.NewSpec(worker), exec.NewHost())
	let ans = process.CallExec[WorkerReq, WorkerAns](er, "echo", WorkerReq{Arg: "exec"}, process.Unlimited) catch {
	case _:
		return fail("CallExec failed")
	}
	if ans.Out != "echo:exec" {
		return fail("unexpected CallExec output: \(ans.Out)")
	}
	let rep = process.PlayExec(er, process.Unlimited, process.Script{Cleanups: 2, Waits: 0ns, Steps: 2, End: process.Gives{Value: 9}}, time.NewManual(0))
	if rep != (process.Report{Ended: process.Gives{Value: 9}, Cleaned: 2}) {
		return fail("PlayExec mismatch")
	}
}

func main() fails os.Exit needs Ambient {
	let args = os.NewHost().Args()
	if args.Len() < 2 {
		return fail("usage: driver <mode> <worker>")
	}
	let mode = args[0]
	let worker = args[1]
	with context { time.Scheduler: time.NewLiveScheduler(), time.Clock: time.NewHost() } {
		match mode {
		case "laws":
			try runLaws(worker)
		case "rpc":
			try runRPC(worker)
		case "kill":
			try runKill(worker)
		case "limits":
			try runLimits(worker)
		case "oom":
			try runOOM(worker)
		case "stack":
			try runStack(worker)
		case "zombies":
			try runRPC(worker)
			try runKill(worker)
			try runOOM(worker)
			try runStack(worker)
			try assertNoZombieChildren()
		case "exec":
			try runExec(worker)
		case _:
			return fail("unknown mode \(mode)")
		}
	}
}
`

func buildProcessChildFixture(t *testing.T) (worker string, driver string) {
	t.Helper()
	if testing.Short() {
		t.Skip("full tier: builds Go")
	}
	root := moduleRootDir(t)
	dir := t.TempDir()
	files := map[string]string{
		"mod/ergo.mod":             "module m\n",
		"mod/cmd/worker/main.ergo": processChildWorkerSrc,
		"mod/cmd/driver/main.ergo": processChildDriverSrc,
	}
	for rel, data := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ergoBin := filepath.Join(dir, "bin", "ergo")
	build := ergobuild.Command(ergoBin, "./cmd/ergo", "-buildvcs=false")
	build.Dir = root
	if out, err := runBound(build); err != nil {
		t.Fatalf("build ergo: %v\n%s", err, out)
	}
	worker = filepath.Join(dir, "bin", "worker")
	driver = filepath.Join(dir, "bin", "driver")
	for _, target := range []struct {
		pkg string
		dst string
	}{
		{"./cmd/worker", worker},
		{"./cmd/driver", driver},
	} {
		compile := exec.Command(ergoBin, "build", target.pkg, "-o", target.dst)
		compile.Dir = filepath.Join(dir, "mod")
		compile.Env = append(os.Environ(), "ERGO_FALLBACK=off")
		if out, err := runBound(compile); err != nil {
			t.Fatalf("ergo build %s: %v\n%s", target.pkg, err, out)
		}
	}
	return worker, driver
}

func runProcessDriver(t *testing.T, mode string) {
	t.Helper()
	worker, driver := buildProcessChildFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, driver, mode, worker)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatalf("start driver %s: %v", mode, err)
	}
	pgid := cmd.Process.Pid
	t.Cleanup(func() {
		if pgid > 0 {
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
		}
	})
	err := cmd.Wait()
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
	if err != nil {
		t.Fatalf("driver %s failed: %v\n%s", mode, err, out.String())
	}
}

func TestStdProcessChildRunnerLaws(t *testing.T) {
	runProcessDriver(t, "laws")
}

func TestStdProcessChildCallRPC(t *testing.T) {
	runProcessDriver(t, "rpc")
}

func TestStdProcessChildKillAlwaysLands(t *testing.T) {
	runProcessDriver(t, "kill")
}

func TestStdProcessChildOverBytesEndsChildOnly(t *testing.T) {
	if runtime.GOOS == "linux" {
		runProcessDriver(t, "limits")
	}
	runProcessDriver(t, "oom")
}

func TestStdProcessChildStackOverflowEndsChildOnly(t *testing.T) {
	runProcessDriver(t, "stack")
}

func TestStdProcessChildZombiesNone(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("/proc zombie inspection runs on linux")
	}
	runProcessDriver(t, "zombies")
}

func TestStdProcessChildWithExec(t *testing.T) {
	runProcessDriver(t, "exec")
}
