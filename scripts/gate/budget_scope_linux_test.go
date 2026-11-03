package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// The systemd scope tests run each case as a gate re-executed as the gate
// itself is (scopeArguments, scopeEnvironment): a helper process, this test
// binary, in a fresh scope capped at the case's gate budget. The scopes run
// in a slice of this test run's own (testSlice), below ergo-gates.slice, so
// the bound a test gives that slice bounds only its own gates, and the real
// gates' bound also holds over them. No test sets the real slice's bound.
// The helpers leave the cgroup of the step that runs this test, if a gate
// runs it: their own small budgets and the gates' slice bound them.

// testSlice is the slice of this test run's gate scopes. When the test
// ends, its helpers have ended, and the slice's MemoryMax, which the test or
// a gate set, is reverted and the empty slice stopped, so the next test
// finds it with no bound.
func testSlice(t *testing.T) string {
	t.Helper()
	return cleanSlice(t, fmt.Sprintf("%s-test-%d.slice", strings.TrimSuffix(gateSlice, ".slice"), os.Getpid()))
}

// cleanSlice reverts and stops slice when the test ends.
func cleanSlice(t *testing.T, slice string) string {
	t.Helper()
	t.Cleanup(func() {
		for _, action := range []string{"revert", "stop"} {
			command := exec.Command("systemctl", "--user", action, slice)
			command.Env = scopeEnvironment("")
			if output, err := command.CombinedOutput(); err != nil {
				t.Logf("systemctl --user %s %s: %v: %s", action, slice, err, output)
			}
		}
	})
	return slice
}

// systemctl runs systemctl --user and returns its output.
func systemctl(t *testing.T, arguments ...string) string {
	t.Helper()
	command := exec.Command("systemctl", append([]string{"--user"}, arguments...)...)
	command.Env = scopeEnvironment("")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("systemctl --user %s: %v: %s", strings.Join(arguments, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

// sliceMemoryMax is the MemoryMax of slice: bytes, or "infinity".
func sliceMemoryMax(t *testing.T, slice string) string {
	t.Helper()
	return systemctl(t, "show", "-p", "MemoryMax", "--value", slice)
}

// boundSlice gives slice a MemoryMax before any gate starts in it, as the
// host's owner would; testSlice reverts it.
func boundSlice(t *testing.T, slice, size string) {
	t.Helper()
	systemctl(t, "set-property", "--runtime", slice, "MemoryMax="+size)
}

// waitForSliceMemory waits until the processes in slice hold at least size
// bytes.
func waitForSliceMemory(t *testing.T, slice string, size int64) {
	t.Helper()
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		current, err := strconv.ParseInt(systemctl(t, "show", "-p", "MemoryCurrent", "--value", slice), 10, 64)
		if err == nil && current >= size {
			return
		}
	}
	t.Fatalf("%s never held %s", slice, formatBytes(size))
}

// defaultHostBound is the bound a gate gives a slice with none: three
// quarters of physical memory, rounded down as the budget line shows it.
func defaultHostBound() int64 {
	return roundedDown(physicalMemory() / 4 * 3)
}

// scopeHelper is one helper gate, set up to run.
type scopeHelper struct {
	command *exec.Cmd
	output  bytes.Buffer
	cache   string
}

// startableScope skips the test where no systemd user scope can start.
func startableScope(t *testing.T, slice string, budget memoryBudget) {
	t.Helper()
	if _, err := exec.LookPath("systemd-run"); err != nil {
		t.Skip("no systemd-run: the RSS watch tests cover this host")
	}
	probe := exec.Command("systemd-run", scopeArguments(slice, "", budget, []string{"true"})...)
	probe.Env = scopeEnvironment("")
	if output, err := probe.CombinedOutput(); err != nil {
		t.Skipf("no systemd user scope here (%v: %s): the RSS watch tests cover this host", err, firstLine(output))
	}
}

// newScopeHelper prepares a helper gate in slice that runs the named case
// under the budget the environment sets.
func newScopeHelper(t *testing.T, slice, name string, budget memoryBudget) *scopeHelper {
	t.Helper()
	unit := scopeUnit()
	h := &scopeHelper{cache: t.TempDir()}
	h.command = exec.Command("systemd-run", scopeArguments(slice, unit, budget, []string{os.Args[0], "-test.run=^TestBudgetScopeHelper$", "-test.count=1"})...)
	h.command.Env = append(scopeEnvironment(unit), "GATE_BUDGET_CASE="+name, "GATE_BUDGET_ROOT="+budgetFixture(t), "GATE_BUDGET_CACHE="+h.cache)
	h.command.Stdout, h.command.Stderr = &h.output, &h.output
	return h
}

// result is the helper gate's exit code and output.
func (h *scopeHelper) result(t *testing.T) (int, string) {
	t.Helper()
	text := h.output.String()
	t.Logf("helper output:\n%s", text)
	_, exit, ok := strings.Cut(text, "gate-exit=")
	var code int
	if _, err := fmt.Sscanf(exit, "%d", &code); !ok || err != nil {
		t.Fatalf("the helper gate did not finish:\n%s", text)
	}
	return code, text
}

// TestBudgetSystemdScope runs the budget cases under the systemd scope, the
// mechanism of Linux hosts with a user service manager. The slice starts
// with no bound, so the first gate gives it the default host bound, not the
// host budget its environment sets, which lowers only its own budgets.
func TestBudgetSystemdScope(t *testing.T) {
	slice := testSlice(t)
	bound := defaultHostBound()
	for _, c := range budgetCases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv(stepMemoryVariable, c.step)
			t.Setenv(gateMemoryVariable, c.gate)
			t.Setenv(hostMemoryVariable, "4G")
			budget, err := currentBudget()
			if err != nil {
				t.Fatal(err)
			}
			startableScope(t, slice, budget)
			h := newScopeHelper(t, slice, c.name, budget)
			h.command.Run()
			code, text := h.result(t)
			if !strings.Contains(text, "gate: memory budget: systemd scope "+c.step+" per step, "+c.gate+" per gate, "+formatBytes(bound)+" across gates in "+slice+"\n") {
				t.Errorf("the helper gate does not name the systemd scope and its budgets")
			}
			if limit := sliceMemoryMax(t, slice); limit != strconv.FormatInt(bound, 10) {
				t.Errorf("the slice's MemoryMax is %s, want the default host bound %d", limit, bound)
			}
			c.check(t, scopeMechanism, code, text, receiptsIn(t, h.cache))
		})
	}
}

// hostCase is a gate whose step holds 448 MiB, under its step and gate
// budgets of 512M; TestBudgetHostSlice runs two at once in a slice bound
// at 768M.
var hostCase = budgetCase{
	name: "host", step: "512M", gate: "512M",
	steps: func() []*Step { return []*Step{allocatorStep("alloc", 448, 3*time.Second)} },
}

// holdCase is a gate whose step holds 448 MiB for 6 seconds, while
// TestBudgetHostBudgets starts another gate.
var holdCase = budgetCase{
	name:  "hold",
	steps: func() []*Step { return []*Step{allocatorStep("alloc", 448, 6*time.Second)} },
}

// TestBudgetHostSlice runs two gates, each under its own budgets, that
// together pass the bound of their slice: the kernel stops the larger, so
// the gates never hold more than the bound together, and the gate stopped
// says so. Each gate reports the bound the slice has, not the host budget
// it would have set, and leaves it as it was.
func TestBudgetHostSlice(t *testing.T) {
	slice := testSlice(t)
	t.Setenv(stepMemoryVariable, hostCase.step)
	t.Setenv(gateMemoryVariable, hostCase.gate)
	t.Setenv(hostMemoryVariable, "")
	budget, err := currentBudget()
	if err != nil {
		t.Fatal(err)
	}
	startableScope(t, slice, budget)
	boundSlice(t, slice, "768M")
	helpers := []*scopeHelper{newScopeHelper(t, slice, hostCase.name, budget), newScopeHelper(t, slice, hostCase.name, budget)}
	var running sync.WaitGroup
	for _, h := range helpers {
		running.Go(func() { h.command.Run() })
	}
	running.Wait()
	if limit := sliceMemoryMax(t, slice); limit != strconv.FormatInt(768<<20, 10) {
		t.Errorf("the slice's MemoryMax is %s after the gates, want 768M as before", limit)
	}
	stopped := 0
	for _, h := range helpers {
		code, text := h.result(t)
		wantAll(t, text, "gate: memory budget: systemd scope 512M per step, 512M per gate, 768M across gates in "+slice+"\n")
		switch {
		case strings.Contains(text, "gate: alloc: host memory budget exceeded (768M)"):
			stopped++
			if code != 1 || len(receiptsIn(t, h.cache)) != 0 {
				t.Errorf("a gate stopped for the host budget exited %d with receipts: want 1 and none", code)
			}
		case code != 0 || !strings.Contains(text, "== alloc: ok"):
			t.Errorf("a gate neither passed nor was stopped for the host budget:\n%s", text)
		}
	}
	if stopped == 0 {
		t.Error("no gate was stopped for the host budget")
	}
}

// TestBudgetHostBudgets runs two gates at once with different host budgets
// in a slice bound at 1G. ERGO_GATE_HOST_MEMORY lowers only its own gate's
// budgets: the slice keeps its bound, the first gate, which holds 448 MiB
// while the second runs, passes, and the second is stopped for its own
// budget of 256M (its step's or its gate's: the scope also holds the gate,
// so it can reach its bound first). Each ends with its own result, and the
// first says that
// the slice's bound is below its gate budget.
func TestBudgetHostBudgets(t *testing.T) {
	slice := testSlice(t)
	t.Setenv(stepMemoryVariable, "512M")
	t.Setenv(gateMemoryVariable, "2G")
	t.Setenv(hostMemoryVariable, "")
	first, err := currentBudget()
	if err != nil {
		t.Fatal(err)
	}
	startableScope(t, slice, first)
	boundSlice(t, slice, "1G")
	before := sliceMemoryMax(t, slice)
	holder := newScopeHelper(t, slice, holdCase.name, first)
	t.Setenv(hostMemoryVariable, "256M")
	second, err := currentBudget()
	if err != nil {
		t.Fatal(err)
	}
	lower := newScopeHelper(t, slice, hostCase.name, second)
	if err := holder.command.Start(); err != nil {
		t.Fatal(err)
	}
	waitForSliceMemory(t, slice, 448<<20)
	lower.command.Run()
	holder.command.Wait()
	if after := sliceMemoryMax(t, slice); after != before {
		t.Errorf("the slice's MemoryMax went from %s to %s", before, after)
	}
	code, text := holder.result(t)
	wantAll(t, text, "gate: memory budget: systemd scope 512M per step, 2G per gate, 1G across gates in "+slice+", below this gate's 2G\n", "== alloc: ok")
	if code != 0 {
		t.Errorf("the first gate exited %d, want 0", code)
	}
	code, text = lower.result(t)
	wantAll(t, text, "gate: memory budget: systemd scope 256M per step, 256M per gate, 1G across gates in "+slice+"\n", "budget exceeded (256M)", "gate: FAILED in")
	if code != 1 {
		t.Errorf("the second gate exited %d, want 1", code)
	}
}

// A gate whose slice cannot be bounded keeps its scope, with its step and
// gate caps, and says it has no bound across gates: when systemctl cannot
// set the bound of a slice with none, or when the scope is in another
// slice.
func TestBudgetUnboundedHost(t *testing.T) {
	t.Setenv(stepMemoryVariable, "512M")
	t.Setenv(gateMemoryVariable, "2G")
	t.Setenv(hostMemoryVariable, "")
	budget, err := currentBudget()
	if err != nil {
		t.Fatal(err)
	}
	run := func(t *testing.T, slice, note string) {
		t.Helper()
		h := newScopeHelper(t, slice, "under", budget)
		h.command.Run()
		code, text := h.result(t)
		wantAll(t, text, "gate: memory budget: systemd scope 512M per step, 2G per gate, no bound across gates ("+note+")\n", "== alloc: ok")
		if code != 0 {
			t.Errorf("exit %d, want 0", code)
		}
	}
	t.Run("set fails", func(t *testing.T) {
		slice := testSlice(t)
		startableScope(t, slice, budget)
		bin := t.TempDir()
		if err := os.WriteFile(filepath.Join(bin, "systemctl"), []byte("#!/bin/sh\necho refused by the test >&2\nexit 1\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
		run(t, slice, "systemctl --user set-property "+slice+" failed (exit status 1): refused by the test")
	})
	t.Run("other slice", func(t *testing.T) {
		slice := cleanSlice(t, fmt.Sprintf("ergo-other-test-%d.slice", os.Getpid()))
		startableScope(t, slice, budget)
		run(t, slice, "the scope is in "+slice+", not in "+gateSlice)
	})
}

// The gate names the budget that stopped a step from the cgroup counters:
// the step's own when the step reached its bound; else the gate's when the
// scope reached its own bound; else the host's when only the slice reached
// its own bound, at the bound the slice has when the step ends; else the
// gate's. memory.events counts a cgroup's descendants too (another gate's
// scope in the slice), so only memory.events.local says which bound was
// reached.
func TestBudgetVerdict(t *testing.T) {
	budget := memoryBudget{step: 512 << 20, gate: 2 << 30, host: 47 << 30}
	kill := "oom 0\noom_kill 1\n"
	for _, c := range []struct {
		name, step string
		// scope and slice are the oom counts of memory.events.local and
		// memory.events when the step ends; both are 0 when it starts.
		scope, slice [2]int
		want         string
	}{
		{"step", "oom 1\noom_kill 1\n", [2]int{0, 1}, [2]int{0, 1}, stepExceeded(budget)},
		{"none", "oom 0\noom_kill 0\n", [2]int{}, [2]int{}, ""},
		{"gate", kill, [2]int{1, 1}, [2]int{0, 1}, gateExceeded(budget)},
		{"host", kill, [2]int{}, [2]int{1, 1}, hostExceeded(768 << 20)},
		{"gate and host", kill, [2]int{1, 1}, [2]int{1, 2}, gateExceeded(budget)},
		{"another gate's bound", kill, [2]int{}, [2]int{0, 1}, gateExceeded(budget)},
		{"unexplained", kill, [2]int{}, [2]int{}, gateExceeded(budget)},
	} {
		t.Run(c.name, func(t *testing.T) {
			slice := t.TempDir()
			scope := filepath.Join(slice, "ergo-gate-1-00000000.scope")
			step := filepath.Join(scope, "step-1")
			counts := func(dir string, local, all int) {
				write(t, dir, "memory.events.local", fmt.Sprintf("oom %d\noom_kill 0\n", local))
				write(t, dir, "memory.events", fmt.Sprintf("oom %d\noom_kill 0\n", all))
			}
			counts(scope, 0, 0)
			counts(slice, 0, 0)
			write(t, slice, "memory.max", strconv.Itoa(768<<20)+"\n")
			write(t, step, "memory.events", "oom 0\noom_kill 0\n")
			write(t, step, "cgroup.procs", "")
			write(t, step, "cgroup.kill", "")
			handle, err := os.Open(step)
			if err != nil {
				t.Fatal(err)
			}
			limiter := &scopeLimiter{budget: budget, dir: scope, slice: slice}
			watch := &scopeStep{limiter: limiter, dir: step, handle: handle, gateOOM: localOOM(scope), hostOOM: localOOM(slice)}
			counts(scope, c.scope[0], c.scope[1])
			counts(slice, c.slice[0], c.slice[1])
			write(t, step, "memory.events", c.step)
			if got := watch.finish().verdict; got != c.want {
				t.Errorf("verdict %q, want %q", got, c.want)
			}
		})
	}
}

// main runs -record-durations in the gate's systemd scope, as it runs a
// gate: the run's budget line names the scope, or, when a gate runs this
// test, the outer gate step whose caps hold, and never says the run was
// not started in a systemd scope.
func TestBudgetRecordDurationsScope(t *testing.T) {
	if _, err := exec.LookPath("systemd-run"); err != nil {
		t.Skip("no systemd-run: the RSS watch tests cover this host")
	}
	binary := filepath.Join(t.TempDir(), "gate")
	if output, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, output)
	}
	root := fixture(t)
	write(t, root, "go.mod", "module example.test/alloc\n\ngo 1.24\n")
	write(t, root, "cmd/ergo/alloc_test.go", allocatingTest)
	command := exec.Command(binary, "-record-durations")
	command.Dir = root
	command.Env = append(os.Environ(), stepMemoryVariable+"=512M", gateMemoryVariable+"=2G", hostMemoryVariable+"=", mechanismVariable+"=", scopeVariable+"=")
	output, err := command.CombinedOutput()
	text := string(output)
	t.Logf("gate -record-durations: %v\n%s", err, text)
	wantAll(t, text, "gate: go test -json ./cmd/ergo: memory budget exceeded (512M)\n")
	if !strings.Contains(text, "gate: memory budget: systemd scope 512M per step, 2G per gate, ") && !strings.Contains(text, "of an outer gate, whose caps hold)\n") {
		t.Errorf("-record-durations did not run in the gate's scope")
	}
}

// TestBudgetScopeHelper is the gate the scope tests start in a systemd
// scope: it runs one case's steps and prints the gate's output and exit
// code. Like the gate, it keeps the scope's unit from its steps.
func TestBudgetScopeHelper(t *testing.T) {
	name := os.Getenv("GATE_BUDGET_CASE")
	if name == "" {
		t.Skip("run by the scope tests")
	}
	scope := os.Getenv(scopeVariable)
	os.Unsetenv(scopeVariable)
	for _, c := range append(budgetCases, hostCase, holdCase) {
		if c.name == name {
			var out bytes.Buffer
			g := &gate{root: os.Getenv("GATE_BUDGET_ROOT"), cacheDir: os.Getenv("GATE_BUDGET_CACHE"), steps: c.steps(), scope: scope}
			code := g.run(&out)
			fmt.Printf("%sgate-exit=%d\n", out.String(), code)
			return
		}
	}
	t.Fatalf("no budget case %q", name)
}

// TestBudgetNestedHelper starts a gate inside a step of the helper gate, as
// main does, and prints where it runs. A gate that opened its own scope
// re-executes this test in it, which then says so.
func TestBudgetNestedHelper(t *testing.T) {
	if os.Getenv("GATE_BUDGET_NESTED") == "" {
		t.Skip("run as a step by the nested budget case")
	}
	if os.Getenv(scopeVariable) != "" {
		fmt.Println("re-executed in a scope")
		return
	}
	note := enterScope(memoryBudget{step: 256 << 20, gate: 512 << 20, host: 512 << 20})
	membership, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("nested note: %s\nnested cgroup: %s", note, membership)
}

// A process is in an outer gate's step when its cgroup is a step-N child of
// an ergo-gate-*.scope; the gate's own leaf and other cgroups are not steps.
func TestBudgetOuterGateStep(t *testing.T) {
	scope := "/user.slice/user-1000.slice/user@1000.service/ergo.slice/ergo-gates.slice/ergo-gate-12-ab12cd34.scope"
	for _, c := range []struct {
		membership string
		nested     bool
	}{
		{"0::" + scope + "/step-7\n", true},
		{"0::" + scope + "/step-7/inner\n", true},
		{"0::" + scope + "/gate\n", false},
		{"0::" + scope + "\n", false},
		{"0::/user.slice/user-1000.slice/session-3.scope\n", false},
		{"0::/user.slice/ergo-gate-1.service/step-1\n", false},
		{"1:name=systemd:" + scope + "/step-7\n", false},
		{"", false},
	} {
		if _, nested := outerGateStep(c.membership); nested != c.nested {
			t.Errorf("outerGateStep(%q) = %t, want %t", c.membership, nested, c.nested)
		}
	}
}
