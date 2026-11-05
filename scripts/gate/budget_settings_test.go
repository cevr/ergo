package main

import (
	"os"
	"slices"
	"strings"
	"testing"
)

// Sizes read and print as systemd's MemoryMax writes them.
func TestBudgetSizes(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		text  string
		bytes int64
		shown string
	}{
		{"512M", 512 << 20, "512M"},
		{"8G", 8 << 30, "8G"},
		{"8g", 8 << 30, "8G"},
		{"1T", 1 << 40, "1T"},
		{"1536K", 1536 << 10, "1.5M"},
		{"4096", 4096, "4K"},
		{"100", 100, "100"},
	} {
		got, err := parseBytes(c.text)
		if err != nil || got != c.bytes || formatBytes(got) != c.shown {
			t.Errorf("parseBytes(%q) = %d, %v, shown %q; want %d shown %q", c.text, got, err, formatBytes(got), c.bytes, c.shown)
		}
	}
	for _, bad := range []string{"", "0", "-1G", "8GB", "G", "1.5G", "99999999999T"} {
		if got, err := parseBytes(bad); err == nil {
			t.Errorf("parseBytes(%q) = %d, want an error", bad, got)
		}
	}
}

// The step budget defaults to 8G, the gate budget to half of physical
// memory and the host budget to three quarters; the variables override
// them, a budget above the one that holds it is lowered to it, and a value
// that does not parse stops the gate.
func TestBudgetFromEnvironment(t *testing.T) {
	t.Setenv(stepMemoryVariable, "")
	t.Setenv(gateMemoryVariable, "")
	t.Setenv(hostMemoryVariable, "")
	budget, err := currentBudget()
	if err != nil {
		t.Fatal(err)
	}
	half, threeQuarters := physicalMemory()/2, physicalMemory()/4*3
	if budget.step != min(8<<30, budget.gate) || budget.gate > half || budget.gate < half-(1<<30) || budget.host > threeQuarters || budget.host < threeQuarters-(1<<30) {
		t.Errorf("default budget %+v; physical memory %d", budget, physicalMemory())
	}
	t.Setenv(stepMemoryVariable, "2G")
	t.Setenv(gateMemoryVariable, "6G")
	t.Setenv(hostMemoryVariable, "12G")
	if budget, err := currentBudget(); err != nil || budget != (memoryBudget{step: 2 << 30, gate: 6 << 30, host: 12 << 30}) {
		t.Errorf("budget %+v, %v; want 2G per step, 6G per gate, 12G per host", budget, err)
	}
	t.Setenv(stepMemoryVariable, "16G")
	if budget, err := currentBudget(); err != nil || budget.step != 6<<30 {
		t.Errorf("budget %+v, %v; want the step budget lowered to the gate's 6G", budget, err)
	}
	t.Setenv(hostMemoryVariable, "4G")
	if budget, err := currentBudget(); err != nil || budget != (memoryBudget{step: 4 << 30, gate: 4 << 30, host: 4 << 30}) {
		t.Errorf("budget %+v, %v; want the gate and step budgets lowered to the host's 4G", budget, err)
	}
	t.Setenv(hostMemoryVariable, "a lot")
	if _, err := currentBudget(); err == nil || !strings.Contains(err.Error(), hostMemoryVariable) {
		t.Errorf("an unreadable host budget gave %v, want an error naming %s", err, hostMemoryVariable)
	}
	t.Setenv(hostMemoryVariable, "")
	t.Setenv(stepMemoryVariable, "lots")
	if _, err := currentBudget(); err == nil || !strings.Contains(err.Error(), stepMemoryVariable) {
		t.Errorf("an unreadable step budget gave %v, want an error naming %s", err, stepMemoryVariable)
	}
	t.Setenv(stepMemoryVariable, "")
	t.Setenv(mechanismVariable, "cgroup")
	g := &gate{}
	if _, err := g.openLimiter(memoryBudget{step: 1 << 30, gate: 2 << 30}); err == nil {
		t.Error("an unknown mechanism was accepted")
	}
}

// Every step, go test steps included, gets GOMEMLIMIT at three quarters of
// the step budget unless the caller sets it, and the Go test tool digest
// keys it. The mechanism is no input: a cache recorded under the systemd
// scope serves the RSS watch.
func TestBudgetSoftLimit(t *testing.T) {
	t.Setenv("GOMEMLIMIT", "")
	os.Unsetenv("GOMEMLIMIT")
	t.Setenv(stepMemoryVariable, "8G")
	t.Setenv(gateMemoryVariable, "16G")
	want := "GOMEMLIMIT=6442450944"
	if !slices.Contains(stepEnvironment(), want) || !slices.Contains(goTestEnvironment(), want) {
		t.Errorf("steps lack %s", want)
	}
	step := &Step{Name: "go test probe", ToolEnv: []string{"sh"}}
	g := &gate{root: t.TempDir()}
	digest := g.toolDigest(step)
	t.Setenv(mechanismVariable, "rss")
	if g.toolDigest(step) != digest {
		t.Error("forcing the RSS watch changed the tool digest")
	}
	t.Setenv(stepMemoryVariable, "4G")
	if g.toolDigest(step) == digest {
		t.Error("a new step budget, so a new GOMEMLIMIT, left the tool digest unchanged")
	}
	t.Setenv("GOMEMLIMIT", "1GiB")
	if environment := goTestEnvironment(); !slices.Contains(environment, "GOMEMLIMIT=1GiB") || slices.ContainsFunc(environment, func(entry string) bool {
		return strings.HasPrefix(entry, "GOMEMLIMIT=") && entry != "GOMEMLIMIT=1GiB"
	}) {
		t.Errorf("the caller's GOMEMLIMIT did not win: %v", environment)
	}
}

func TestStepTempDirectoryIsStablePerCheckoutAndStep(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	root := t.TempDir()
	name := "go test package [1/2]"
	first := stepTempDir(root, name)
	if first != stepTempDir(root, name) {
		t.Fatal("the step temp path changed across calls")
	}
	if first == stepTempDir(root, "go test package [2/2]") {
		t.Fatal("two steps share a temp path")
	}
	if !strings.HasPrefix(first, tmp+string(os.PathSeparator)) {
		t.Fatalf("step temp path %q is outside the caller's temp directory %q", first, tmp)
	}
	want := "TMPDIR=" + first
	step := &Step{Name: name, Argv: []string{"go", "test"}}
	if !slices.Contains(stepEnvironmentFor(root, step), want) || !slices.Contains(stepCommandEnvironment(root, step), want) {
		t.Fatalf("the step environments do not use their stable temp path %q", want)
	}
}
