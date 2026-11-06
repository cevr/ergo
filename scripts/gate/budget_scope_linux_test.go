package main

import (
	"bytes"
	"fmt"
	"io"
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

// startedSlices are the slices in which startableScope started a scope in
// this test process. A slice in which no scope started was never created,
// so cleanSlice leaves it alone: where no user manager is reachable (the
// merge tier's sandbox has no user bus), systemctl could only fail.
var startedSlices sync.Map

// cleanSlice reverts and stops slice when the test ends, if a scope started
// in it.
func cleanSlice(t *testing.T, slice string) string {
	t.Helper()
	t.Cleanup(func() {
		if _, started := startedSlices.Load(slice); !started {
			return
		}
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
	// unit is the scope's unit name.
	unit string
}

// startableScope skips the test where no systemd user scope can start.
func startableScope(t *testing.T, slice string, budget memoryBudget) {
	t.Helper()
	if _, err := exec.LookPath("systemd-run"); err != nil {
		t.Skip("no systemd-run: the RSS watch tests cover this host")
	}
	probe := exec.Command("systemd-run", scopeArguments(slice, "", budget, stepCPUs(), []string{"true"})...)
	probe.Env = scopeEnvironment("")
	if output, err := probe.CombinedOutput(); err != nil {
		t.Skipf("no systemd user scope here (%v: %s): the RSS watch tests cover this host", err, firstLine(output))
	}
	startedSlices.Store(slice, true)
}

// newScopeHelper prepares a helper gate in slice that runs the named case
// under the budgets the environment sets.
func newScopeHelper(t *testing.T, slice, name string, budget memoryBudget) *scopeHelper {
	t.Helper()
	unit := scopeUnit()
	cpus, err := currentCPUs()
	if err != nil {
		t.Fatal(err)
	}
	h := &scopeHelper{cache: t.TempDir(), unit: unit}
	h.command = exec.Command("systemd-run", scopeArguments(slice, unit, budget, cpus, []string{os.Args[0], "-test.run=^TestBudgetScopeHelper$", "-test.count=1"})...)
	h.command.Env = append(scopeGateEnvironment(unit, cpus), "GATE_BUDGET_CASE="+name, "GATE_BUDGET_ROOT="+budgetFixture(t), "GATE_BUDGET_CACHE="+h.cache)
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
			setBudgetCaseTemp(t)
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

// gateOOMCases give the gate and its one step equal memory budgets, so the
// step's memory alone reaches every bound the gate's steps have. The kernel
// must stop the step and never the gate, which must report it.
var gateOOMCases = []budgetCase{
	{
		// The allocator's memory can still be charged, after the kernel
		// killed it, when the gate next needs memory.
		name: "gate-oom", step: "256M", gate: "256M", scopeOnly: true,
		steps: func() []*Step { return []*Step{allocatorStep("alloc", 448, 10*time.Second)} },
	},
	{
		// dd fills a tmpfs file until the step's budget stops it. Without
		// swap the file's pages stay charged where dd wrote them after dd
		// dies, and dd's resident memory is small, so in an out-of-memory
		// domain that holds the gate, the gate is the largest process.
		name: "gate-oom-shm", step: "256M", gate: "256M", scopeOnly: true,
		steps: func() []*Step {
			return []*Step{{Name: "alloc", Argv: []string{"dd", "if=/dev/zero", "of=" + os.Getenv("GATE_BUDGET_SHM"), "bs=1M", "count=512"}}}
		},
	},
}

// TestBudgetGateSurvivesStepOOM runs the equal-budget cases, which used to
// let the out-of-memory killer choose the gate in its scope.
// scopeHelper.result requires a gate-exit line, so an OOM-killed gate fails
// the test.
func TestBudgetGateSurvivesStepOOM(t *testing.T) {
	slice := testSlice(t)
	for _, c := range gateOOMCases {
		t.Run(c.name, func(t *testing.T) {
			setBudgetCaseTemp(t)
			t.Setenv(stepMemoryVariable, c.step)
			t.Setenv(gateMemoryVariable, c.gate)
			t.Setenv(hostMemoryVariable, "4G")
			budget, err := currentBudget()
			if err != nil {
				t.Fatal(err)
			}
			startableScope(t, slice, budget)
			if info, err := os.Stat("/dev/shm"); err != nil || !info.IsDir() {
				t.Skipf("no /dev/shm tmpfs (%v)", err)
			}
			shm := filepath.Join("/dev/shm", scopeUnit()+".fill")
			t.Cleanup(func() { _ = os.Remove(shm) })
			t.Setenv("GATE_BUDGET_SHM", shm)
			h := newScopeHelper(t, slice, c.name, budget)
			if err := h.command.Run(); err != nil {
				t.Logf("scope helper exited %v", err)
			}
			code, output := h.result(t)
			wantAll(t, output, "== alloc: FAILED", "memory budget exceeded (256M)", "gate: FAILED in")
			if code != 1 {
				t.Errorf("gate exit %d, want the failed step to keep the gate alive and exit 1:\n%s", code, output)
			}
		})
	}
}

// gateResidentLimit is the most resident memory (VmHWM) a gateOutputCase
// helper gate may reach. The gate's own is far under it: measured 7.8M to
// 10M on 26 helper gates (2026-11-05), most of it the test binary's code. A
// gate that held the step's output in its memory would reach at least the
// output's size, outputLines' twice this limit or more
// (TestBudgetOutputCaseSize).
const gateResidentLimit = 14 << 20

// outputLines is how many lines gateOutputCase's step prints: 30888896
// bytes (seqBytes).
const outputLines = 4_000_000

// gateShmemLimit is the most tmpfs memory (memory.stat shmem) the scope of
// a gateOutputCase helper may hold at any sample, and the most bytes any
// open file in memory in the scope may hold (checkPlacement). Pages of files
// on disk are page cache, not shmem.
const gateShmemLimit = 4 << 20

// gateOutputCase is a step whose memory stays far under its 64M budget while
// it prints 31 MB. The gate must hold no more for it than for any step: its
// resident memory must stay under gateResidentLimit (gateResident). The
// step declares inputs, so its pass is recorded and the helper's second run
// replays it from the receipt.
var gateOutputCase = budgetCase{
	name: "gate-output", step: "64M", gate: "64M", scopeOnly: true,
	steps: func() []*Step {
		return []*Step{{Name: "print", Argv: []string{"seq", "1", strconv.Itoa(outputLines)}, Inputs: &Inputs{Paths: []string{"src/"}}}}
	},
}

// gateResident checks the resident memory a gateOutputCase helper gate
// reports (runOutputHelper) against gateResidentLimit.
func gateResident(t *testing.T, run int, text string) {
	t.Helper()
	_, value, ok := strings.Cut(text, "gate-resident-peak=")
	var peak int64
	if _, err := fmt.Sscanf(value, "%d", &peak); !ok || err != nil {
		t.Errorf("run %d: the helper gate reported no resident peak", run)
		return
	}
	if peak > gateResidentLimit {
		t.Errorf("run %d: the gate's resident memory reached %d bytes, over %d: it held step output in its memory", run, peak, int64(gateResidentLimit))
	}
}

// residentPeak is this process's peak resident memory (VmHWM) in bytes, or
// -1 if /proc does not say.
func residentPeak() int64 {
	status, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return -1
	}
	for _, line := range strings.Split(string(status), "\n") {
		if rest, ok := strings.CutPrefix(line, "VmHWM:"); ok {
			fields := strings.Fields(rest)
			if len(fields) == 2 && fields[1] == "kB" {
				if kilobytes, err := strconv.ParseInt(fields[0], 10, 64); err == nil {
					return kilobytes << 10
				}
			}
		}
	}
	return -1
}

// seqBytes is the size of `seq 1 n`'s output.
func seqBytes(n int64) int64 {
	var size int64
	for digits, low := int64(1), int64(1); low <= n; digits, low = digits+1, low*10 {
		size += (min(n, low*10-1) - low + 1) * (digits + 1)
	}
	return size
}

// TestBudgetGateSurvivesStepOutput runs gateOutputCase in a real scope, as
// two gates one after the other on one cache: the first executes the step
// and records it, the second replays the receipt. Each must finish
// (gate-exit=0), print every line in order and stay under
// gateResidentLimit, and no output spool or step temp file may remain.
func TestBudgetGateSurvivesStepOutput(t *testing.T) {
	if _, err := exec.LookPath("seq"); err != nil {
		t.Skip("seq is unavailable")
	}
	slice := testSlice(t)
	caseTemp := setBudgetCaseTemp(t)
	t.Setenv(stepMemoryVariable, gateOutputCase.step)
	t.Setenv(gateMemoryVariable, gateOutputCase.gate)
	t.Setenv(hostMemoryVariable, "4G")
	budget, err := currentBudget()
	if err != nil {
		t.Fatal(err)
	}
	startableScope(t, slice, budget)
	// Each run is a gate of its own in a scope of its own, on one checkout
	// and one cache.
	root, cache := budgetFixture(t), t.TempDir()
	checked := fmt.Sprintf("lines=%d broken=\"\"\n", outputLines)
	for run, want := range []string{"== print: ok (", "== print: ok, cached ("} {
		h := newScopeHelper(t, slice, gateOutputCase.name, budget)
		for name, value := range map[string]string{"GATE_BUDGET_ROOT": root, "GATE_BUDGET_CACHE": cache, "GATE_OUTPUT_RUN": strconv.Itoa(run + 1)} {
			h.command.Env = setEnvironmentVariable(h.command.Env, name, value)
		}
		report := runWatched(t, h)
		code, text := h.result(t)
		wantAll(t, text, want, fmt.Sprintf("seq-check run=%d %s", run+1, checked))
		if code != 0 {
			t.Errorf("run %d: gate exit %d, want 0", run+1, code)
		}
		gateResident(t, run+1, text)
		checkPlacement(t, run+1, report, true, false)
	}
	receipts := receiptsIn(t, cache)
	if len(receipts) != 1 {
		t.Fatalf("receipts %v, want the one pass", receipts)
	}
	if info, err := os.Stat(strings.TrimSuffix(receipts[0], ".json") + ".out"); err != nil || info.Size() != seqBytes(outputLines) {
		t.Errorf("recorded output: %v, %v; want %d bytes", info, err, seqBytes(outputLines))
	}
	spools, err := os.ReadDir(filepath.Join(cache, "output"))
	if err != nil || len(spools) != 0 {
		t.Errorf("output spools remain: %v, %v", spools, err)
	}
	stepTemps, err := os.ReadDir(filepath.Join(caseTemp, "ergo-gate", "tmp"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range stepTemps {
		if !entry.IsDir() {
			continue
		}
		if left, err := os.ReadDir(filepath.Join(caseTemp, "ergo-gate", "tmp", entry.Name())); err != nil || len(left) != 0 {
			t.Errorf("step temp dir %s not emptied: %v, %v", entry.Name(), left, err)
		}
	}
}

// seqCheck reads a gate's output as the gate prints it, holding a bounded
// amount: the lines of digits must be 1, 2, 3, ... with none missing, and
// the other lines, the gate's own, are kept for the test.
type seqCheck struct {
	partial []byte
	lines   int64
	broken  string
	kept    bytes.Buffer
}

func (c *seqCheck) Write(content []byte) (int, error) {
	n := len(content)
	for len(content) > 0 {
		end := bytes.IndexByte(content, '\n')
		if end < 0 {
			c.partial = append(c.partial, content...)
			if len(c.partial) > 4096 {
				c.fail("a line longer than 4096 bytes")
				c.partial = c.partial[:0]
			}
			break
		}
		line := content[:end]
		if len(c.partial) > 0 {
			c.partial = append(c.partial, line...)
			line = c.partial
		}
		c.line(line)
		c.partial = c.partial[:0]
		content = content[end+1:]
	}
	return n, nil
}

func (c *seqCheck) line(line []byte) {
	var value int64
	for _, b := range line {
		if b < '0' || b > '9' {
			value = -1
			break
		}
		value = value*10 + int64(b-'0')
	}
	if len(line) == 0 || value < 0 {
		if c.kept.Len() < 1<<20 {
			c.kept.Write(line)
			c.kept.WriteByte('\n')
		} else {
			c.fail("more than 1M of other lines")
		}
		return
	}
	if value != c.lines+1 {
		c.fail(fmt.Sprintf("line %d after line %d", value, c.lines))
	}
	c.lines = value
}

func (c *seqCheck) fail(reason string) {
	if c.broken == "" {
		c.broken = reason
	}
}

// runOutputHelper is TestBudgetScopeHelper for gateOutputCase: it runs the
// gate and checks its output as it streams, holding a bounded amount. With
// GATE_OUTPUT_FILE set, the gate prints into that file, as `gate > file`
// does, and the check reads the file when the gate is done.
func runOutputHelper(scope string) {
	var check seqCheck
	g := &gate{root: os.Getenv("GATE_BUDGET_ROOT"), cacheDir: os.Getenv("GATE_BUDGET_CACHE"), steps: gateOutputCase.steps(), scope: scope}
	var code int
	if name := os.Getenv("GATE_OUTPUT_FILE"); name != "" {
		file, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
		if err != nil {
			fmt.Printf("open the gate's output file: %v\n", err)
			return
		}
		code = g.run(file)
		if err := file.Close(); err != nil {
			fmt.Printf("close the gate's output file: %v\n", err)
			return
		}
		printed, err := os.Open(name)
		if err != nil {
			fmt.Printf("open the gate's output file: %v\n", err)
			return
		}
		_, err = io.CopyBuffer(&check, onlyReader{printed}, make([]byte, 32<<10))
		_ = printed.Close()
		if err != nil {
			fmt.Printf("read the gate's output file: %v\n", err)
			return
		}
	} else {
		code = g.run(&check)
	}
	if len(check.partial) > 0 {
		check.line(check.partial)
	}
	fmt.Printf("%sseq-check run=%s lines=%d broken=%q\ngate-resident-peak=%d\ngate-exit=%d\n", check.kept.String(), os.Getenv("GATE_OUTPUT_RUN"), check.lines, check.broken, residentPeak(), code)
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
// budget of 256M (its step's or its gate's: both bound its one step, and the
// steps' cgroup also holds what earlier steps left charged, so it can reach
// its bound first). Each ends with its own result, and the
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
// steps' cgroup reached its own bound; else the host's when only the slice reached
// its own bound, at the bound the slice has when the step ends; else the
// gate's. memory.events counts a cgroup's descendants too (another gate's
// scope in the slice), so only memory.events.local says which bound was
// reached.
func TestBudgetVerdict(t *testing.T) {
	t.Parallel()
	budget := memoryBudget{step: 512 << 20, gate: 2 << 30, host: 47 << 30}
	kill := "oom 0\noom_kill 1\n"
	for _, c := range []struct {
		name, step string
		// scope and slice are the oom counts of memory.events.local and
		// memory.events, of "steps" (which the gate budget bounds) and of
		// the slice, when the step ends; both are 0 when it starts.
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
			steps := filepath.Join(scope, stepsCgroup)
			step := filepath.Join(steps, "step-1")
			counts := func(dir string, local, all int) {
				write(t, dir, "memory.events.local", fmt.Sprintf("oom %d\noom_kill 0\n", local))
				write(t, dir, "memory.events", fmt.Sprintf("oom %d\noom_kill 0\n", all))
			}
			counts(steps, 0, 0)
			counts(slice, 0, 0)
			write(t, slice, "memory.max", strconv.Itoa(768<<20)+"\n")
			write(t, step, "memory.events", "oom 0\noom_kill 0\n")
			write(t, step, "cgroup.procs", "")
			write(t, step, "cgroup.kill", "")
			write(t, step, "cgroup.events", "populated 0\nfrozen 0\n")
			handle, err := os.Open(step)
			if err != nil {
				t.Fatal(err)
			}
			limiter := &scopeLimiter{budget: budget, dir: scope, steps: steps, slice: slice}
			watch := &scopeStep{limiter: limiter, dir: step, handle: handle, gateOOM: localOOM(steps), hostOOM: localOOM(slice)}
			counts(steps, c.scope[0], c.scope[1])
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
	// Without VCS stamping: its git status reads every tracked file of the
	// checkout, none an input of this test (hostPackageInputs).
	if output, err := exec.Command("go", "build", "-buildvcs=false", "-o", binary, ".").CombinedOutput(); err != nil {
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
	scope, err := takeScope()
	if err != nil {
		t.Fatal(err)
	}
	if name == gateOutputCase.name {
		runOutputHelper(scope)
		return
	}
	if name == oracleCase {
		if err := runOracleHelper(scope); err != nil {
			fmt.Printf("oracle: %v\noracle-exit=1\n", err)
			return
		}
		fmt.Printf("oracle-exit=0\n")
		return
	}
	for _, c := range append(append(budgetCases, hostCase, holdCase, cpuCase, quotaCase), gateOOMCases...) {
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

// A step starts only while the bounds that keep the gate out of its steps'
// out-of-memory domain read back: "steps" at the gate budget, and the scope
// at least the gate's reserve above it. Each broken bound refuses the step.
func TestBudgetStepDomainReadBack(t *testing.T) {
	t.Parallel()
	budget := memoryBudget{step: 256 << 20, gate: 256 << 20}
	bound := func(n int64) string { return strconv.FormatInt(n, 10) + "\n" }
	for _, c := range []struct {
		name, scope, steps string
		started            bool
	}{
		{"protected", bound(scopeMemory(budget)), bound(budget.gate), true},
		{"steps unbounded", bound(scopeMemory(budget)), "max\n", false},
		{"steps above the gate budget", bound(scopeMemory(budget)), bound(scopeMemory(budget)), false},
		{"scope at the gate budget", bound(budget.gate), bound(budget.gate), false},
		{"scope unbounded", "max\n", bound(budget.gate), false},
	} {
		t.Run(c.name, func(t *testing.T) {
			scope := t.TempDir()
			steps := filepath.Join(scope, stepsCgroup)
			write(t, scope, "memory.max", c.scope)
			write(t, steps, "memory.max", c.steps)
			limiter := &scopeLimiter{budget: budget, cpus: 2, dir: scope, steps: steps}
			watch, err := limiter.prepare("alloc", exec.Command("true"))
			if !c.started {
				if err == nil {
					t.Fatal("a step started without the gate outside its steps' out-of-memory domain")
				}
				if entries, _ := filepath.Glob(filepath.Join(steps, "step-*")); len(entries) != 0 {
					t.Errorf("a refused step left cgroups %v", entries)
				}
				return
			}
			if err != nil {
				t.Fatalf("prepare with both bounds read back: %v", err)
			}
			step := watch.(*scopeStep)
			if filepath.Dir(step.dir) != steps {
				t.Errorf("step cgroup %s is not a child of %s", step.dir, steps)
			}
			if err := step.handle.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// The scope systemd runs the gate in holds the gate budget for the steps
// and the gate's reserve above it.
func TestBudgetScopeArgumentsReserve(t *testing.T) {
	t.Parallel()
	budget := memoryBudget{step: 256 << 20, gate: 1 << 30}
	arguments := strings.Join(scopeArguments(gateSlice, "u", budget, 2, []string{"true"}), " ")
	if want := "MemoryMax=" + strconv.FormatInt(budget.gate+gateReserve, 10) + " "; !strings.Contains(arguments, want) {
		t.Errorf("systemd-run arguments %q lack %q", arguments, want)
	}
}

func TestBudgetScopedGateCannotFallBackToRSS(t *testing.T) {
	t.Run("forced RSS", func(t *testing.T) {
		t.Setenv(mechanismVariable, "rss")
		if _, err := (&gate{scope: "test-unit"}).openLimiter(memoryBudget{step: 1, gate: 1}); err == nil {
			t.Fatal("a scoped gate ran without verified gate OOM protection")
		}
	})
	t.Run("unusable scope", func(t *testing.T) {
		t.Setenv(mechanismVariable, "")
		if _, err := (&gate{scope: "not-the-current-unit"}).openLimiter(memoryBudget{step: 1, gate: 1}); err == nil {
			t.Fatal("a scoped gate fell back to RSS after scope setup failed")
		}
	})
}

func TestStopStepCgroupWaitsForKernelPopulationZero(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{
		"cgroup.procs":  "",
		"cgroup.kill":   "",
		"cgroup.events": "populated 1\nfrozen 0\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	type result struct {
		members []string
		err     error
	}
	done := make(chan result, 1)
	go func() {
		members, err := stopStepCgroup(dir)
		done <- result{members: members, err: err}
	}()
	select {
	case got := <-done:
		t.Fatalf("stop returned while cgroup.events said populated 1: %+v", got)
	case <-time.After(30 * time.Millisecond):
	}
	updated := filepath.Join(dir, "cgroup.events.updated")
	if err := os.WriteFile(updated, []byte("populated 0\nfrozen 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(updated, filepath.Join(dir, "cgroup.events")); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-done:
		if got.err != nil || len(got.members) != 0 {
			t.Fatalf("stop after populated 0: %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("stop did not observe cgroup.events populated 0")
	}
}

func TestStepCgroupPopulatedRequiresKernelField(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "cgroup.events")
	for _, content := range []string{"frozen 0\n", "populated 2\n"} {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := stepCgroupPopulated(dir); err == nil {
			t.Errorf("accepted cgroup.events %q", content)
		}
	}
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
	budget := memoryBudget{step: 256 << 20, gate: 512 << 20, host: 512 << 20}
	command, reason := scopeProbe(budget, 2)
	note := enterScope(command, reason, budget, 2)
	membership, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("nested note: %s\nnested cgroup: %s", note, membership)
}

// A process is in an outer gate's step when its cgroup is a step-N child of
// the steps cgroup of an ergo-gate-*.scope; the gate's own leaf and other
// cgroups are not steps.
func TestBudgetOuterGateStep(t *testing.T) {
	t.Parallel()
	scope := "/user.slice/user-1000.slice/user@1000.service/ergo.slice/ergo-gates.slice/ergo-gate-12-ab12cd34.scope"
	for _, c := range []struct {
		membership string
		nested     bool
	}{
		{"0::" + scope + "/steps/step-7\n", true},
		{"0::" + scope + "/steps/step-7/inner\n", true},
		{"0::" + scope + "/step-7\n", false},
		{"0::" + scope + "/steps\n", false},
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
