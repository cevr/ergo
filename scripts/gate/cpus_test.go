package main

import (
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// randomCPUMax is a cpu.max content: unbounded, a quota of whole or
// fractional CPUs, or one that does not parse. It returns the CPUs it
// allows, rounded up, or 0 when it bounds nothing.
func randomCPUMax(r *rand.Rand) (string, int) {
	period := []int64{100000, 50000, 1000000}[r.IntN(3)]
	switch r.IntN(5) {
	case 0:
		return fmt.Sprintf("max %d", period), 0
	case 1:
		return []string{"", "garbage", "0 100000", "-5 100000", "100000 0", "1 2 3"}[r.IntN(6)], 0
	default:
		quota := r.Int64N(64*period) + 1
		return fmt.Sprintf("%d %d", quota, period), int((quota + period - 1) / period)
	}
}

// The budget is the variable's value, or half of the usable CPUs and at
// least 2, never more than the usable CPUs, which are the affinity mask's
// CPUs lowered to the tightest cpu.max above the gate. Checked against an
// oracle over random masks, cgroup chains and values.
func TestCPUBudgetProperty(t *testing.T) {
	t.Parallel()
	r := rand.New(rand.NewPCG(1, 2))
	for range 5000 {
		affinity := r.IntN(128) + 1
		var limits []string
		usable := affinity
		for range r.IntN(4) {
			limit, cpus := randomCPUMax(r)
			limits = append(limits, limit)
			if cpus > 0 {
				usable = min(usable, cpus)
			}
		}
		if got := usableCPUs(affinity, limits); got != usable {
			t.Fatalf("usableCPUs(%d, %q) = %d, want %d", affinity, limits, got, usable)
		}
		value, want := "", max(2, usable/2)
		if r.IntN(2) == 0 {
			n := r.IntN(200) + 1
			value, want = strconv.Itoa(n), n
		}
		want = min(want, usable)
		budget, err := cpuBudgetFor(value, usable)
		if err != nil || budget != want {
			t.Fatalf("cpuBudgetFor(%q, %d) = %d, %v; want %d", value, usable, budget, err, want)
		}
		if budget < 1 || budget > usable {
			t.Fatalf("budget %d outside 1..%d", budget, usable)
		}
		if got := stepGOMAXPROCS("", budget); got != strconv.Itoa(budget) {
			t.Fatalf("stepGOMAXPROCS(unset, %d) = %s", budget, got)
		}
	}
}

// A value that is not a positive whole number of CPUs fails closed: the
// gate refuses to start, with one line that names the variable.
func TestCPUBudgetRejectsInvalidValues(t *testing.T) {
	for _, value := range []string{"0", "-1", "x", "1.5", " 2", "2 ", "4CPUs", "65537", "99999999999999999999", "0x10"} {
		t.Run(value, func(t *testing.T) {
			if _, err := cpuBudgetFor(value, 16); err == nil || !strings.HasPrefix(err.Error(), cpuVariable+": ") || strings.Contains(err.Error(), "\n") {
				t.Errorf("cpuBudgetFor(%q) error %v, want one line naming %s", value, err, cpuVariable)
			}
			t.Setenv(cpuVariable, value)
			if _, err := currentCPUs(); err == nil {
				t.Errorf("currentCPUs accepted %s=%q", cpuVariable, value)
			}
		})
	}
}

// Every step's Go processes get GOMAXPROCS at most the budget: the caller's
// value when it is a whole number within the budget, else the budget.
func TestStepGOMAXPROCSIsBoundedByTheBudget(t *testing.T) {
	t.Setenv(cpuVariable, "2")
	t.Setenv("GOMAXPROCS", "")
	budget := stepCPUs()
	for caller, want := range map[string]string{"": strconv.Itoa(budget), "1": "1", "64": strconv.Itoa(budget), "x": strconv.Itoa(budget), "0": strconv.Itoa(budget)} {
		if caller == "" {
			os.Unsetenv("GOMAXPROCS")
		} else {
			t.Setenv("GOMAXPROCS", caller)
		}
		var got []string
		for _, entry := range keyedEnvironment() {
			if value, ok := strings.CutPrefix(entry, "GOMAXPROCS="); ok {
				got = append(got, value)
			}
		}
		if len(got) != 1 || got[0] != want {
			t.Errorf("caller GOMAXPROCS %q: steps get %q, want [%s]", caller, got, want)
		}
	}
}

// At most the CPU budget of steps run at once, and each sees GOMAXPROCS at
// the budget: six independent steps under ERGO_GATE_CPUS=2. Each step marks
// itself running in a shared directory and counts the marks, as it starts
// and after a second.
func TestCPUBudgetBoundsStepsAtOnce(t *testing.T) {
	t.Setenv(cpuVariable, "2")
	t.Setenv("GOMAXPROCS", "")
	os.Unsetenv("GOMAXPROCS")
	budget := stepCPUs()
	running := t.TempDir()
	var steps []*Step
	for i := range 6 {
		steps = append(steps, &Step{
			Name: fmt.Sprintf("s%d", i),
			Argv: []string{"sh", "-c", `mkdir "$0/$1"; a=$(ls "$0" | wc -l); sleep 1; b=$(ls "$0" | wc -l); rmdir "$0/$1"; echo "at-once $a $b gomaxprocs=$GOMAXPROCS"`, running, strconv.Itoa(i)},
		})
	}
	code, out := runGate(t, fixture(t), t.TempDir(), steps)
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	wantAll(t, out, fmt.Sprintf("gate: cpu budget: %d CPUs, ", budget))
	most, seen := 0, 0
	for line := range strings.Lines(out) {
		var a, b int
		var gomaxprocs string
		if _, err := fmt.Sscanf(line, "at-once %d %d %s", &a, &b, &gomaxprocs); err != nil {
			continue
		}
		seen++
		most = max(most, a, b)
		if gomaxprocs != "gomaxprocs="+strconv.Itoa(budget) {
			t.Errorf("a step ran with %s, want gomaxprocs=%d", gomaxprocs, budget)
		}
	}
	if seen != 6 || most > budget {
		t.Errorf("%d steps reported, up to %d at once; want 6, at most %d:\n%s", seen, most, budget, out)
	}
	if entries, _ := os.ReadDir(running); len(entries) != 0 {
		t.Errorf("marks remain in %s: %v", filepath.Base(running), entries)
	}
}
