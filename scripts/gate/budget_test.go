package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
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
			if paths := stepTempArtifacts(t); len(paths) != 0 {
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
	for _, line := range strings.Split(out, "\n") {
		if _, value, ok := strings.Cut(line, "leftover child pid="); ok {
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

func stepTempArtifacts(t *testing.T) []string {
	t.Helper()
	var artifacts []string
	err := filepath.WalkDir(os.TempDir(), func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() && filepath.Clean(path) == filepath.Clean(testScratch) {
			return filepath.SkipDir
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
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("ERGO_GATE_STEP_MEMORY", c.step)
			t.Setenv("ERGO_GATE_MEMORY", c.gate)
			cache := t.TempDir()
			code, out := runGate(t, budgetFixture(t), cache, c.steps())
			t.Logf("gate exit %d:\n%s", code, out)
			if !strings.Contains(out, "gate: memory budget: RSS watch "+c.step+" per step, "+c.gate+" per gate, no bound across gates (forced by ERGO_GATE_MEMORY_MECHANISM=rss)\n") {
				t.Errorf("the gate does not name the RSS watch and its budget:\n%s", out)
			}
			c.check(t, rssMechanism, code, out, receiptsIn(t, cache))
		})
	}
}
