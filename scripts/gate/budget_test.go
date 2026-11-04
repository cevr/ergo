package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// The memory budget, tested through the gate's run as a caller sees it:
// the environment sets the budgets, and steps run this test binary as an
// allocator (TestBudgetAllocator). This file names no gate internals, so it
// also runs against a gate without a budget, where it fails.

// TestBudgetAllocator is a step's process: run with two arguments, it
// allocates and touches that many MiB, prints "allocated", and holds the
// memory for the given duration. It prints "held" only after the full hold.
func TestBudgetAllocator(t *testing.T) {
	if flag.NArg() != 2 {
		t.Skip("a budget test runs it as a step")
	}
	mib, err := strconv.Atoi(flag.Arg(0))
	if err != nil {
		t.Fatal(err)
	}
	hold, err := time.ParseDuration(flag.Arg(1))
	if err != nil {
		t.Fatal(err)
	}
	var chunks [][]byte
	for range mib / 16 {
		chunk := make([]byte, 16<<20)
		for i := 0; i < len(chunk); i += 4096 {
			chunk[i] = 1
		}
		chunks = append(chunks, chunk)
	}
	fmt.Printf("allocated %d MiB\n", mib)
	time.Sleep(hold)
	runtime.KeepAlive(chunks)
	fmt.Printf("held %d MiB\n", mib)
}

// allocatorStep allocates mib MiB and holds it. The shell around the
// allocator exits 0 even when the allocator is killed, so a verdict cannot
// come from the exit status alone. Its result is cacheable, so a test can
// see that a budget failure is never recorded.
func allocatorStep(name string, mib int, hold time.Duration) *Step {
	return &Step{
		Name:   name,
		Argv:   []string{"sh", "-c", `"$0" -test.run='^TestBudgetAllocator$' -test.count=1 "$1" "$2"; echo "allocator exited $?"`, os.Args[0], strconv.Itoa(mib), hold.String()},
		Inputs: &Inputs{Paths: []string{"src/" + name + ".txt"}},
	}
}

// The mechanisms a budget case runs under.
const (
	rssMechanism   = "rss"
	scopeMechanism = "scope"
)

// budgetCase is one gate run under a budget, checked on every mechanism
// unless scopeOnly.
type budgetCase struct {
	name, step, gate string
	scopeOnly        bool
	steps            func() []*Step
	check            func(t *testing.T, mechanism string, code int, out string, receipts []string)
}

// stepStopped checks that the gate stopped its one step, alloc, for the
// step budget, went on and recorded no pass.
func stepStopped(t *testing.T, code int, out string, receipts []string) {
	t.Helper()
	wantAll(t, out, "== alloc: FAILED", "gate: alloc: memory budget exceeded (512M)\n", "gate: FAILED in")
	if code != 1 || len(receipts) != 0 {
		t.Errorf("exit %d, receipts %v: want 1 and no receipt:\n%s", code, receipts, out)
	}
}

var budgetCases = []budgetCase{
	{
		// The acceptance case: a 2 GB allocation under a 512M step budget is
		// stopped during its hold, the gate goes on, reports it and records no pass.
		name: "over", step: "512M", gate: "2G",
		steps: func() []*Step {
			return []*Step{allocatorStep("alloc", 2048, 10*time.Second), {Name: "independent", Argv: []string{"sh", "-c", "echo independent ran"}}}
		},
		check: func(t *testing.T, _ string, code int, out string, receipts []string) {
			stepStopped(t, code, out, receipts)
			wantAll(t, out, "== independent: ok", "independent ran")
			if strings.Contains(out, "held 2048 MiB") {
				t.Errorf("the allocator held its memory through the full interval:\n%s", out)
			}
		},
	},
	{
		// A step at one and a half times its budget is stopped too: the
		// bound is the budget, not a multiple of it.
		name: "boundary", step: "512M", gate: "2G",
		steps: func() []*Step { return []*Step{allocatorStep("alloc", 768, 5*time.Second)} },
		check: func(t *testing.T, _ string, code int, out string, receipts []string) {
			stepStopped(t, code, out, receipts)
			if strings.Contains(out, "held 768 MiB") {
				t.Errorf("the allocator held its memory through the full interval:\n%s", out)
			}
		},
	},
	{
		// A process of the step that stays under the budget is stopped with
		// the one that passes it. The leader owns the release writer and
		// releases the survivor only after allocation completes. EOF means the
		// leader died first, which is tolerated only for the systemd scope's
		// per-task group kill.
		name: "whole", step: "512M", gate: "2G",
		steps: func() []*Step {
			return []*Step{{
				Name: "alloc",
				Argv: []string{"sh", "-c", `mkfifo src/survivor-ready src/survivor-release
( exec 3<src/survivor-release; echo "survivor started"; echo ready > src/survivor-ready; if read -r _ <&3; then echo "survivor finished"; else echo "survivor orphaned"; fi ) &
exec 4>src/survivor-release
read -r _ < src/survivor-ready
echo "step pgid=$$"
"$0" -test.run='^TestBudgetAllocator$' -test.count=1 2048 10s 4>&-
allocatorStatus=$?
echo "allocator exited $allocatorStatus"
echo release >&4
wait
echo "step exited"`, os.Args[0]},
				Inputs: &Inputs{Paths: []string{"src/alloc.txt"}},
			}}
		},
		check: func(t *testing.T, mechanism string, code int, out string, receipts []string) {
			stepStopped(t, code, out, receipts)
			if !strings.Contains(out, "survivor started") {
				t.Errorf("the survivor did not signal that it started before allocation:\n%s", out)
			}
			if strings.Contains(out, "step exited") {
				t.Errorf("the leader ran on after the budget stopped the step:\n%s", out)
			}
			switch mechanism {
			case rssMechanism:
				if strings.Contains(out, "survivor orphaned") {
					t.Errorf("RSS killed only the leader before the survivor:\n%s", out)
				}
				fallthrough
			case scopeMechanism:
				if strings.Contains(out, "survivor finished") {
					t.Errorf("the survivor ran after allocation returned:\n%s", out)
				}
			default:
				t.Fatalf("unknown budget mechanism %q", mechanism)
			}
		},
	},
	{
		// A step under its budget passes, and the gate reports its peak.
		name: "under", step: "512M", gate: "2G",
		steps: func() []*Step { return []*Step{allocatorStep("alloc", 64, time.Second)} },
		check: func(t *testing.T, _ string, code int, out string, receipts []string) {
			wantAll(t, out, "allocated 64 MiB", "allocator exited 0")
			if code != 0 || !strings.Contains(out, "== alloc: ok (") || !strings.Contains(out, ", peak ") || len(receipts) != 1 {
				t.Errorf("exit %d, receipts %v: want a pass with its peak, recorded once:\n%s", code, receipts, out)
			}
		},
	},
	{
		// Two steps, each under the step budget, together pass the gate
		// budget: the gate stops the largest, so its steps never hold more
		// than the gate budget together. The RSS watch stops exactly one;
		// under the systemd scope the kernel may stop the other too, when
		// the first one's memory is not yet freed as the second one grows.
		name: "gate", step: "512M", gate: "768M",
		steps: func() []*Step {
			return []*Step{allocatorStep("a", 448, 3*time.Second), allocatorStep("b", 448, 3*time.Second)}
		},
		check: func(t *testing.T, mechanism string, code int, out string, receipts []string) {
			stopped := 0
			for _, name := range []string{"a", "b"} {
				if strings.Contains(out, "gate: "+name+": gate memory budget exceeded (768M)") {
					stopped++
				} else if !strings.Contains(out, "== "+name+": ok") {
					t.Errorf("step %s neither passed nor was stopped for the gate budget", name)
				}
			}
			if code != 1 || stopped == 0 || len(receipts) != 2-stopped || mechanism == rssMechanism && stopped != 1 {
				t.Errorf("exit %d, %d steps stopped for the gate budget under the %s mechanism, receipts %v: want 1, one step (either on the scope), and one receipt per passed step:\n%s", code, stopped, mechanism, receipts, out)
			}
		},
	},
	{
		// A gate started inside a step of this one opens no scope of its own:
		// it stays in the step's cgroup, whose caps hold.
		// TestBudgetNestedHelper is that gate's start.
		name: "nested", step: "512M", gate: "2G", scopeOnly: true,
		steps: func() []*Step {
			return []*Step{{Name: "nested", Argv: []string{"sh", "-c", `GATE_BUDGET_NESTED=1 "$0" -test.run='^TestBudgetNestedHelper$' -test.count=1 -test.v`, os.Args[0]}}}
		},
		check: func(t *testing.T, _ string, code int, out string, _ []string) {
			wantAll(t, out, "nested note: inside step cgroup ", "of an outer gate, whose caps hold", "nested cgroup: 0::/", "/step-")
			if code != 0 || strings.Contains(out, "re-executed in a scope") {
				t.Errorf("exit %d: want 0, and the nested gate in the outer step's cgroup:\n%s", code, out)
			}
		},
	},
	{
		// A child that keeps output open after its leader exits fails the
		// step, and the scope stops the child before the gate drains output.
		name: "leftover", step: "512M", gate: "2G", scopeOnly: true,
		steps: func() []*Step { return []*Step{leftoverStep()} },
		check: func(t *testing.T, _ string, code int, out string, receipts []string) {
			wantAll(t, out, "== alloc: FAILED", "leftover processes at leader exit:")
			pid := leftoverPID(out)
			if pid == 0 || !namedLeftover(out, pid) {
				t.Errorf("the leftover diagnostic does not name child pid %d:\n%s", pid, out)
			}
			if code != 1 || len(receipts) != 0 {
				t.Errorf("exit %d, receipts %v: want the leftover process to fail the step without a receipt:\n%s", code, receipts, out)
			}
			if pid := leftoverPID(out); pid > 0 && processExists(pid) {
				t.Errorf("leftover process %d still exists after the scope stopped the step", pid)
			}
		},
	},
	{
		// A step killed by its memory budget loses every file it wrote in
		// its stable, gate-owned temp directory, including TestMain's temp.
		name: "temp", step: "512M", gate: "2G",
		steps: func() []*Step { return []*Step{tempAllocatorStep()} },
		check: func(t *testing.T, _ string, code int, out string, receipts []string) {
			stepStopped(t, code, out, receipts)
			if strings.Contains(out, "step output held open") {
				t.Errorf("the OOM-killed allocator has no outside writer, but output was reported held open:\n%s", out)
			}
			if paths := stepTempArtifacts(t, os.TempDir()); len(paths) != 0 {
				t.Errorf("step temporary files outlived the step: %v", paths)
			}
		},
	},
}

func leftoverStep() *Step {
	return &Step{Name: "alloc", Argv: []string{"sh", "-c", `echo "$$" > src/leftover-pgid
sleep 60 &
child=$!
echo "$child" > src/leftover-pid
echo "leftover child pid=$child"
exit 0`}}
}

func tempAllocatorStep() *Step {
	return &Step{
		Name: "alloc",
		Argv: []string{"sh", "-c", `printf 'created\n' > "$TMPDIR/leftovers-step-marker"
"$0" -test.run='^TestBudgetAllocator$' -test.count=1 2048 10s 4>&-
echo "allocator exited $?"`, os.Args[0]},
		Inputs: &Inputs{Paths: []string{"src/alloc.txt"}},
	}
}

func leftoverPID(out string) int {
	return leftoverPIDFor(out, "leftover child pid=")
}

func leftoverPIDFor(out, marker string) int {
	for _, line := range strings.Split(out, "\n") {
		if _, value, ok := strings.Cut(line, marker); ok {
			pid, _ := strconv.Atoi(strings.TrimSpace(value))
			return pid
		}
	}
	return 0
}

func namedLeftover(out string, pid int) bool {
	marker := "leftover processes at leader exit: "
	pidSuffix := fmt.Sprintf("(pid %d)", pid)
	for _, line := range strings.Split(out, "\n") {
		_, members, ok := strings.Cut(line, marker)
		if !ok {
			continue
		}
		for _, member := range strings.Split(members, ", ") {
			if strings.HasSuffix(member, pidSuffix) && strings.TrimSpace(strings.TrimSuffix(member, pidSuffix)) != "" {
				return true
			}
		}
	}
	return false
}

func processExists(pid int) bool {
	output, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "stat=").Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return false
		}
		return true
	}
	state := strings.TrimSpace(string(output))
	return state != "" && !strings.HasPrefix(state, "Z") && !strings.HasPrefix(state, "X")
}

func stopProcess(pid int) {
	if pid <= 0 {
		return
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return
	}
	if processExists(pid) {
		_ = process.Kill()
	}
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if !processExists(pid) {
			return
		}
	}
}

func stepTempArtifacts(t *testing.T, caseTemp string) []string {
	t.Helper()
	var artifacts []string
	err := filepath.WalkDir(caseTemp, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Name() == "leftovers-step-marker" || entry.IsDir() && strings.HasPrefix(entry.Name(), "gate-tools-") {
			artifacts = append(artifacts, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return artifacts
}

func setBudgetCaseTemp(t *testing.T) string {
	t.Helper()
	caseTemp := t.TempDir()
	for _, name := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(name, caseTemp)
	}
	return caseTemp
}

// Another test's leftover in the shared temp directory, named as the temp
// case's own artifacts are, does not fail the temp case: it checks only the
// temp directory of its own gate.
func TestBudgetTempCaseIgnoresSharedTemp(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the RSS watch requires Unix process groups")
	}
	strayDir, err := os.MkdirTemp(os.TempDir(), "gate-oom-shared-stray-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(strayDir) })
	write(t, strayDir, "leftovers-step-marker", "another test's artifact\n")
	if err := os.Mkdir(filepath.Join(strayDir, "gate-tools-stray"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ERGO_GATE_MEMORY_MECHANISM", "rss")
	for _, c := range budgetCases {
		if c.name == "temp" {
			runRSSCase(t, c)
			return
		}
	}
	t.Fatal("no temp budget case")
}

func TestBudgetRSSWatchStopsLeaderExitDescendant(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the RSS watch requires Unix process groups")
	}
	t.Setenv(mechanismVariable, "rss")
	t.Setenv(stepMemoryVariable, "512M")
	t.Setenv(gateMemoryVariable, "2G")
	root := budgetFixture(t)
	cache := t.TempDir()
	done := make(chan struct {
		code int
		out  string
	}, 1)
	go func() {
		code, out := runGate(t, root, cache, []*Step{leftoverStep()})
		done <- struct {
			code int
			out  string
		}{code, out}
	}()

	childPath := filepath.Join(root, "src", "leftover-pid")
	var childPID int
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if content, err := os.ReadFile(childPath); err == nil {
			childPID, _ = strconv.Atoi(strings.TrimSpace(string(content)))
			if childPID > 0 {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if childPID == 0 {
		t.Fatal("step did not record the background child's PID")
	}

	select {
	case result := <-done:
		wantAll(t, result.out, "== alloc: FAILED", "leftover processes at leader exit:")
		if !namedLeftover(result.out, childPID) {
			t.Errorf("the leftover diagnostic does not name child pid %d:\n%s", childPID, result.out)
		}
		if result.code != 1 || processExists(childPID) {
			t.Fatalf("exit %d, leftover process alive=%t:\n%s", result.code, processExists(childPID), result.out)
		}
	case <-time.After(10 * time.Second):
		child, err := os.FindProcess(childPID)
		if err == nil {
			_ = child.Kill()
		}
		select {
		case result := <-done:
			t.Fatalf("gate did not stop the descendant promptly; after test cleanup it returned %d:\n%s", result.code, result.out)
		case <-time.After(3 * time.Second):
			t.Fatal("gate remained blocked after the test stopped its own descendant")
		}
	}
}

func TestBudgetRSSWatchDetectsEscapedOutputHolder(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the RSS watch requires Unix process groups")
	}
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("setsid is unavailable")
	}
	t.Setenv(mechanismVariable, rssMechanism)
	t.Setenv(stepMemoryVariable, "512M")
	t.Setenv(gateMemoryVariable, "2G")
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "tmp"))
	root := budgetFixture(t)
	var childPID int
	t.Cleanup(func() { stopProcess(childPID) })
	pidPath := filepath.Join(root, "src", "escaped-pid")
	step := &Step{Name: "escaped", Argv: []string{"sh", "-c", `setsid sleep 60 & child=$!; echo "$child" > src/escaped-pid; echo "escaped output holder pid=$child"; exit 0`}}
	done := make(chan struct {
		code int
		out  string
	}, 1)
	cache := t.TempDir()
	go func() {
		code, out := runGate(t, root, cache, []*Step{step})
		done <- struct {
			code int
			out  string
		}{code, out}
	}()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if content, err := os.ReadFile(pidPath); err == nil {
			childPID, _ = strconv.Atoi(strings.TrimSpace(string(content)))
			if childPID > 0 {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if childPID == 0 {
		t.Fatal("the step did not record the escaped output holder PID")
	}
	var code int
	var out string
	select {
	case result := <-done:
		code, out = result.code, result.out
	case <-time.After(3 * time.Second):
		stopProcess(childPID)
		select {
		case result := <-done:
			t.Fatalf("the RSS gate waited for the escaped writer until test cleanup, then returned %d:\n%s", result.code, result.out)
		case <-time.After(2 * time.Second):
			t.Fatal("the RSS gate remained blocked after test cleanup stopped the escaped writer")
		}
	}
	childPID = leftoverPIDFor(out, "escaped output holder pid=")
	wantAll(t, out, "== escaped: FAILED", "step output held open by a process outside the step's process group", "gate: FAILED in")
	if childPID == 0 {
		t.Fatalf("the escaped output holder pid was not reported:\n%s", out)
	}
	if code != 1 {
		t.Fatalf("exit %d, want the escaped output holder to fail the step:\n%s", code, out)
	}
	if !processExists(childPID) {
		t.Fatalf("escaped output holder %d was not alive when the gate returned:\n%s", childPID, out)
	}
}

type passThroughLimiter struct{}

func (passThroughLimiter) describe() string { return "pass-through test limiter" }

func (passThroughLimiter) prepare(string, *exec.Cmd) (stepWatch, error) {
	return passThroughWatch{}, nil
}

func (passThroughLimiter) close() {}

type passThroughWatch struct{}

func (passThroughWatch) started(int) {}

func (passThroughWatch) finish() usage { return usage{} }

type outputThroughputLimiter struct{}

func (outputThroughputLimiter) describe() string { return "output-throughput test limiter" }

func (outputThroughputLimiter) prepare(string, *exec.Cmd) (stepWatch, error) {
	return outputThroughputWatch{}, nil
}

func (outputThroughputLimiter) close() {}

type outputThroughputWatch struct{}

func (outputThroughputWatch) started(int) {}

func (outputThroughputWatch) finish() usage { return usage{} }

type outputCounter struct{ bytes int64 }

func (w *outputCounter) Write(content []byte) (int, error) {
	w.bytes += int64(len(content))
	return len(content), nil
}

func TestRunBudgetedOutputThroughput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the output pipe requires Unix process groups")
	}

	const outputBytes = 50 << 20
	const generousLimit = 1500 * time.Millisecond
	var output outputCounter
	command := exec.Command("head", "-c", strconv.Itoa(outputBytes), "/dev/zero")
	command.Stdout = &output
	command.Stderr = &output

	started := time.Now()
	if _, err := runBudgeted(outputThroughputLimiter{}, "output-throughput", command); err != nil {
		t.Fatalf("runBudgeted: %v", err)
	}
	elapsed := time.Since(started)
	t.Logf("runBudgeted shared output: %d bytes in %s (generous limit %s)", output.bytes, elapsed, generousLimit)
	if output.bytes != outputBytes {
		t.Fatalf("captured %d bytes, want %d", output.bytes, outputBytes)
	}
	if elapsed > generousLimit {
		t.Fatalf("50 MB output took %s, above the generous %s limit", elapsed, generousLimit)
	}
}

// This test and the drain tests below stay serial, as runGate's tests do:
// a program another parallel test starts would hold the write ends of their
// output pipes between its fork and its exec.
func TestRunBudgetedSharedWritersPreserveRandomInterleavings(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the property uses a Unix shell")
	}
	random := rand.New(rand.NewSource(20261103))
	for trial := range 12 {
		var script, want strings.Builder
		counts := [2]int{}
		for range 160 {
			stream := random.Intn(2)
			token := fmt.Sprintf("%c-%03d", "oe"[stream], counts[stream])
			counts[stream]++
			fmt.Fprintf(&script, "printf '%%s\\n' '%s'", token)
			if stream == 1 {
				script.WriteString(" >&2")
			}
			script.WriteByte('\n')
			want.WriteString(token)
			want.WriteByte('\n')
		}
		command := exec.Command("sh", "-c", script.String())
		var output bytes.Buffer
		command.Stdout = &output
		command.Stderr = &output
		if _, err := runBudgeted(passThroughLimiter{}, "order", command); err != nil {
			t.Fatalf("trial %d: runBudgeted: %v", trial, err)
		}
		if got := output.String(); got != want.String() {
			t.Fatalf("trial %d: shared output changed write order\n got: %q\nwant: %q", trial, got, want.String())
		}
	}
}

func TestRunBudgetedDetectsAWriterAfterStepFinish(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the output pipe requires Unix process groups")
	}
	pidPath := filepath.Join(t.TempDir(), "child-pid")
	var childPID int
	t.Cleanup(func() { stopProcess(childPID) })
	command := exec.Command("sh", "-c", `sleep 60 & echo $! > "$1"; echo leader exiting; exit 0`, "sh", pidPath)
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	type runResult struct {
		used usage
		err  error
	}
	done := make(chan runResult, 1)
	go func() {
		used, err := runBudgeted(passThroughLimiter{}, "held-pipe", command)
		done <- runResult{used: used, err: err}
	}()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if content, err := os.ReadFile(pidPath); err == nil {
			childPID, _ = strconv.Atoi(strings.TrimSpace(string(content)))
			if childPID > 0 {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if childPID == 0 {
		t.Fatal("the step did not record its output-holding child")
	}
	select {
	case result := <-done:
		if result.err != nil || !strings.Contains(result.used.processIssue, "step output held open") {
			t.Fatalf("result %+v, want an output-holder process issue", result)
		}
	case <-time.After(3 * time.Second):
		stopProcess(childPID)
		select {
		case result := <-done:
			t.Fatalf("runBudgeted waited for the output writer until test cleanup, then returned %+v", result)
		case <-time.After(2 * time.Second):
			t.Fatal("runBudgeted remained blocked after test cleanup stopped the output writer")
		}
	}
}

// failingWriter fails every write, as an output spool on a full disk does.
type failingWriter struct{ writes int }

func (w *failingWriter) Write([]byte) (int, error) {
	w.writes++
	return 0, errors.New("no space left on device")
}

// A destination that fails stops the copy, not the read: the pipe is read to
// its end, so a step that prints more than the pipe holds still finishes,
// and the failure is the drain's result.
func TestDrainPipeDrainsAfterAWriteError(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	printed := make(chan error, 1)
	go func() {
		// Sixteen times a 64K pipe buffer: the writer finishes only if the
		// reader keeps reading.
		_, err := writer.Write(bytes.Repeat([]byte("output\n"), 16*64<<10/7))
		if closeErr := writer.Close(); err == nil {
			err = closeErr
		}
		printed <- err
	}()
	var destination failingWriter
	var mu sync.Mutex
	drained := make(chan pipeResult, 1)
	go drainPipe(reader, &destination, &mu, drained)
	select {
	case err := <-printed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the step's write blocked: the drain stopped reading at the write error")
	}
	result := <-drained
	if !result.eof || result.err == nil || result.err.Error() != "no space left on device" {
		t.Fatalf("drain result %+v, want EOF and the write error", result)
	}
	if destination.writes != 1 {
		t.Fatalf("destination written %d times, want once: no write after it failed", destination.writes)
	}
}

func TestDrainPipeReachesEOFAfterStepStop(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the output pipe requires Unix process groups")
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if err := makePipeReaderNonblocking(reader); err != nil {
		t.Skipf("nonblocking pipe reader is unsupported: %v", err)
	}
	payload := []byte("last output\n")
	if _, err := writer.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := reader.SetReadDeadline(time.Now()); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	var mu sync.Mutex
	drained := make(chan pipeResult, 1)
	go drainPipe(reader, &output, &mu, drained)
	result := <-drained
	if !result.eof || result.heldOpen || result.err != nil {
		t.Fatalf("post-stop drain result %+v, want EOF without held-open or error", result)
	}
	if got := output.String(); got != string(payload) {
		t.Fatalf("post-stop output %q, want %q", got, payload)
	}
}

func TestCommandOutputReachesEOFAfterStepStop(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the output pipe requires Unix process groups")
	}
	command := exec.Command("sh", "-c", "printf 'last output\\n'")
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	captured, err := captureCommandOutput(command)
	if err != nil {
		t.Fatal(err)
	}
	watch, err := passThroughLimiter{}.prepare("eof", command)
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		captured.closeWriters()
		_ = captured.wait()
		t.Fatal(err)
	}
	watch.started(command.Process.Pid)
	captured.closeWriters()
	commandErr := command.Wait()
	used := watch.finish()
	eof, heldOpen, drainErr := captured.finish()
	captured.restore()
	if commandErr != nil || used.stopFailed || drainErr != nil || !eof || heldOpen {
		t.Fatalf("after step stop: command error %v, usage %+v, EOF %t, held open %t, drain error %v", commandErr, used, eof, heldOpen, drainErr)
	}
	if got := output.String(); got != "last output\n" {
		t.Fatalf("output %q, want the complete step output", got)
	}
}

func TestGateSerializesSameStepTempAcrossGates(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("step temp flocking requires Unix")
	}
	t.Setenv(mechanismVariable, rssMechanism)
	t.Setenv(stepMemoryVariable, "512M")
	t.Setenv(gateMemoryVariable, "2G")
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "tmp"))
	root := budgetFixture(t)
	stepTemp := stepTempDir(root, "shared")
	firstStep := &Step{Name: "shared", Argv: []string{"sh", "-c", `printf first > "$TMPDIR/marker"; sleep 0.4; test -f "$TMPDIR/marker" || { echo 'marker removed while first gate ran'; exit 1; }; echo first kept marker`}}
	secondStep := &Step{Name: "shared", Argv: []string{"sh", "-c", `test ! -e "$TMPDIR/marker" || { echo 'second gate saw stale marker'; exit 1; }; echo second gate ran`}}
	type gateResult struct {
		code int
		out  string
	}
	firstCache, secondCache := t.TempDir(), t.TempDir()
	firstDone := make(chan gateResult, 1)
	go func() {
		code, out := runGate(t, root, firstCache, []*Step{firstStep})
		firstDone <- gateResult{code: code, out: out}
	}()
	marker := filepath.Join(stepTemp, "marker")
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("first gate did not create its temp marker")
	}
	secondDone := make(chan gateResult, 1)
	go func() {
		code, out := runGate(t, root, secondCache, []*Step{secondStep})
		secondDone <- gateResult{code: code, out: out}
	}()
	var first, second gateResult
	select {
	case first = <-firstDone:
	case <-time.After(8 * time.Second):
		t.Fatal("first gate did not finish")
	}
	select {
	case second = <-secondDone:
	case <-time.After(8 * time.Second):
		t.Fatal("second gate did not finish after acquiring the step temp lock")
	}
	if first.code != 0 || !strings.Contains(first.out, "first kept marker") {
		t.Fatalf("first gate lost its temp file: exit %d\n%s", first.code, first.out)
	}
	if second.code != 0 || !strings.Contains(second.out, "second gate ran") {
		t.Fatalf("second gate did not run after the first released its temp directory: exit %d\n%s", second.code, second.out)
	}
}

func TestGateCleansReadOnlyStepTempBeforeAndAfterRun(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("step temp cleanup requires Unix permissions")
	}
	t.Setenv(mechanismVariable, rssMechanism)
	t.Setenv(stepMemoryVariable, "512M")
	t.Setenv(gateMemoryVariable, "2G")
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "tmp"))
	root := budgetFixture(t)
	tmpDir := stepTempDir(root, "read-only")
	locked := filepath.Join(tmpDir, "locked")
	t.Cleanup(func() {
		_ = os.Chmod(locked, 0o700)
		_ = os.RemoveAll(tmpDir)
	})
	if err := os.MkdirAll(locked, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(locked, "stale"), []byte("stale\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o500); err != nil {
		t.Fatal(err)
	}
	step := &Step{Name: "read-only", Argv: []string{"sh", "-c", `test -z "$(ls -A "$TMPDIR")" || { echo 'stale temp content remained'; exit 1; }; mkdir "$TMPDIR/locked"; printf fresh > "$TMPDIR/locked/file"; chmod 0500 "$TMPDIR/locked"; echo 'step started clean'`}}
	code, out := runGate(t, root, t.TempDir(), []*Step{step})
	if code != 0 || !strings.Contains(out, "step started clean") {
		t.Fatalf("exit %d, gate did not clean the stale read-only temp directory before launch:\n%s", code, out)
	}
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("step temp contents survived cleanup: %v", entries)
	}
}

type failedStopLimiter struct{}

func (failedStopLimiter) describe() string { return "failed stop test limiter" }

func (failedStopLimiter) prepare(string, *exec.Cmd) (stepWatch, error) {
	return failedStopWatch{}, nil
}

func (failedStopLimiter) close() {}

type failedStopWatch struct{}

func (failedStopWatch) started(int) {}

func (failedStopWatch) finish() usage {
	return usage{processIssue: "could not stop test process", stopFailed: true}
}

func TestRunBudgetedDoesNotWaitForOutputAfterStopFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the probe requires a Unix shell")
	}
	pidPath := filepath.Join(t.TempDir(), "child-pid")
	stopChild := func() {
		content, err := os.ReadFile(pidPath)
		if err != nil {
			return
		}
		childPID, _ := strconv.Atoi(strings.TrimSpace(string(content)))
		if childPID > 0 {
			child, err := os.FindProcess(childPID)
			if err == nil {
				_ = child.Kill()
			}
		}
	}
	t.Cleanup(stopChild)
	command := exec.Command("sh", "-c", `sleep 60 & echo $! > "$1"; exit 0`, "sh", pidPath)
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	type result struct {
		used usage
		err  error
	}
	done := make(chan result, 1)
	started := time.Now()
	go func() {
		used, err := runBudgeted(failedStopLimiter{}, "probe", command)
		done <- result{used: used, err: err}
	}()

	var childPID int
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if content, err := os.ReadFile(pidPath); err == nil {
			childPID, _ = strconv.Atoi(strings.TrimSpace(string(content)))
			if childPID > 0 {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if childPID == 0 {
		t.Fatal("step did not record the background child's PID")
	}
	select {
	case result := <-done:
		stopChild()
		if result.err != nil || !result.used.stopFailed || time.Since(started) > 2*time.Second {
			t.Fatalf("runBudgeted result %+v after %s; want prompt return after stop failure", result, time.Since(started))
		}
	case <-time.After(2 * time.Second):
		stopChild()
		select {
		case result := <-done:
			t.Fatalf("runBudgeted waited for descendant output after stop failure: %+v", result)
		case <-time.After(3 * time.Second):
			t.Fatal("runBudgeted remained blocked after the test stopped its own descendant")
		}
	}
}

func wantAll(t *testing.T, out string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

// receiptsIn lists the passes recorded in a gate cache directory.
func receiptsIn(t *testing.T, cache string) []string {
	t.Helper()
	receipts, err := filepath.Glob(filepath.Join(cache, "*", "*", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	return receipts
}

// budgetFixture is a fixture with the inputs the allocator steps declare.
func budgetFixture(t *testing.T) string {
	root := fixture(t)
	for _, name := range []string{"alloc", "a", "b"} {
		write(t, root, "src/"+name+".txt", name+"\n")
	}
	return root
}

// TestBudgetRSSWatch runs the cases under the RSS watch, the mechanism of
// darwin and of hosts without a usable systemd scope, forced here so it
// runs on Linux too.
func TestBudgetRSSWatch(t *testing.T) {
	t.Setenv("ERGO_GATE_MEMORY_MECHANISM", "rss")
	for _, c := range budgetCases {
		if c.scopeOnly {
			continue
		}
		t.Run(c.name, func(t *testing.T) { runRSSCase(t, c) })
	}
}

// runRSSCase runs one budget case under the RSS watch, in a temp directory
// of its own.
func runRSSCase(t *testing.T, c budgetCase) {
	t.Helper()
	setBudgetCaseTemp(t)
	t.Setenv("ERGO_GATE_STEP_MEMORY", c.step)
	t.Setenv("ERGO_GATE_MEMORY", c.gate)
	cache := t.TempDir()
	code, out := runGate(t, budgetFixture(t), cache, c.steps())
	t.Logf("gate exit %d:\n%s", code, out)
	if !strings.Contains(out, "gate: memory budget: RSS watch "+c.step+" per step, "+c.gate+" per gate, no bound across gates (forced by ERGO_GATE_MEMORY_MECHANISM=rss)\n") {
		t.Errorf("the gate does not name the RSS watch and its budget:\n%s", out)
	}
	c.check(t, rssMechanism, code, out, receiptsIn(t, cache))
}
