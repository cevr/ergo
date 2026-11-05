package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"
	"unsafe"
)

// The gate's scope carries exactly one CPUQuota, the budget's: 100% a CPU.
func TestScopeArgumentsCarryTheCPUQuota(t *testing.T) {
	t.Parallel()
	budget := memoryBudget{step: 256 << 20, gate: 1 << 30}
	for _, cpus := range []int{1, 2, 3, 8, 64} {
		var quotas []string
		for _, argument := range scopeArguments(gateSlice, "u", budget, cpus, []string{"true"}) {
			if quota, ok := strings.CutPrefix(argument, "CPUQuota="); ok {
				quotas = append(quotas, quota)
			}
		}
		if want := fmt.Sprintf("%d%%", cpus*100); len(quotas) != 1 || quotas[0] != want {
			t.Errorf("budget %d: CPUQuota %q, want [%s]", cpus, quotas, want)
		}
	}
}

// schedAffinity reads or sets the calling thread's affinity mask.
func schedAffinity(call uintptr, mask *[1024]uint64) error {
	if _, _, errno := syscall.RawSyscall(call, 0, uintptr(len(mask)*8), uintptr(unsafe.Pointer(&mask[0]))); errno != 0 {
		return errno
	}
	return nil
}

// The usable CPUs follow the affinity mask, not the CPUs the host has:
// a gate started under taskset -c 0,1 may use 2.
func TestUsableCPUsFollowAffinity(t *testing.T) {
	limits := cgroupCPULimits()
	if usableCPUs(runtime.NumCPU(), limits) < 3 {
		t.Skipf("%d CPUs here (cgroup %q): no smaller mask to tell apart", runtime.NumCPU(), limits)
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	var saved, two [1024]uint64
	if err := schedAffinity(syscall.SYS_SCHED_GETAFFINITY, &saved); err != nil {
		t.Fatal(err)
	}
	for word, picked := 0, 0; word < len(saved) && picked < 2; word++ {
		for bit := 0; bit < 64 && picked < 2; bit++ {
			if saved[word]&(1<<bit) != 0 {
				two[word] |= 1 << bit
				picked++
			}
		}
	}
	if err := schedAffinity(syscall.SYS_SCHED_SETAFFINITY, &two); err != nil {
		t.Skipf("sched_setaffinity: %v", err)
	}
	got := readUsableCPUs()
	if err := schedAffinity(syscall.SYS_SCHED_SETAFFINITY, &saved); err != nil {
		t.Fatal(err)
	}
	if want := usableCPUs(2, limits); got != want {
		t.Errorf("usable CPUs under a 2-CPU mask: %d, want %d", got, want)
	}
}

// cpuProbeVariable makes TestCPUProbe print what a step's Go process sees.
const cpuProbeVariable = "GATE_CPU_PROBE"

// TestCPUProbe is a step of cpuCase: it prints its cgroup's cpu.max, the
// scope's (two cgroups up: scope/steps/step-N), and its GOMAXPROCS; as the
// child it prints only GOMAXPROCS.
func TestCPUProbe(t *testing.T) {
	switch os.Getenv(cpuProbeVariable) {
	case "":
		t.Skip("cpuCase runs it as a step")
	case "child":
		fmt.Printf("cpu-probe child gomaxprocs=%d\n", runtime.GOMAXPROCS(0))
		return
	}
	membership, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		t.Fatal(err)
	}
	_, path, _ := strings.Cut(strings.TrimSpace(string(membership)), "0::")
	leaf := filepath.Join(cgroupRoot, path)
	read := func(dir string) string {
		content, err := os.ReadFile(filepath.Join(dir, "cpu.max"))
		if err != nil {
			return err.Error()
		}
		return strings.TrimSpace(string(content))
	}
	fmt.Printf("cpu-probe leaf=%q scope=%q gomaxprocs=%d\n", read(leaf), read(filepath.Dir(filepath.Dir(leaf))), runtime.GOMAXPROCS(0))
}

// cpuCase is a gate whose one step probes the CPU budget, then runs a Go
// child with no GOMAXPROCS in its environment.
var cpuCase = budgetCase{
	name: "cpu", scopeOnly: true,
	steps: func() []*Step {
		return []*Step{{Name: "probe", Argv: []string{"sh", "-c", cpuProbeVariable + `=1 "$0" -test.run='^TestCPUProbe$' -test.count=1 && env -u GOMAXPROCS ` + cpuProbeVariable + `=child "$0" -test.run='^TestCPUProbe$' -test.count=1`, os.Args[0]}}}
	},
}

// quotaCase is a gate whose one step passes only when its cgroup's cpu.max
// allows one CPU. It declares inputs and only sh, so its pass is recorded
// and no tool fingerprint holds the budget.
var quotaCase = budgetCase{
	name: "quota", scopeOnly: true,
	steps: func() []*Step {
		return []*Step{{
			Name:   "quota-sensitive",
			Argv:   []string{"sh", "-c", `read member < /proc/self/cgroup; path=${member#0::}; read quota period < "/sys/fs/cgroup$path/cpu.max"; echo "quota=$quota"; test "$quota" = 100000`},
			Inputs: &Inputs{Paths: []string{"src/"}, Tools: []string{"sh"}},
		}}
	},
}

// A step's receipt recorded in a real scope at budget 1 is not replayed at
// budget 2, where the step's quota differs: the gate on the shared cache
// runs it and fails, as a gate on a fresh cache does (the verifier's
// TestVerifierScopedReceipt, round 1).
func TestCPUBudgetScopedReceipt(t *testing.T) {
	slice := testSlice(t)
	setBudgetCaseTemp(t)
	t.Setenv(stepMemoryVariable, "512M")
	t.Setenv(gateMemoryVariable, "1G")
	t.Setenv(hostMemoryVariable, "4G")
	t.Setenv("GOMAXPROCS", "1")
	budget, err := currentBudget()
	if err != nil {
		t.Fatal(err)
	}
	startableScope(t, slice, budget)
	if usableCPUsOnce() < 2 {
		t.Skip("one usable CPU: no second budget")
	}
	root, shared := budgetFixture(t), t.TempDir()
	run := func(cpus, cache string) (int, string) {
		t.Helper()
		t.Setenv(cpuVariable, cpus)
		h := newScopeHelper(t, slice, quotaCase.name, budget)
		for name, value := range map[string]string{"GATE_BUDGET_ROOT": root, "GATE_BUDGET_CACHE": cache} {
			h.command.Env = setEnvironmentVariable(h.command.Env, name, value)
		}
		h.command.Run()
		return h.result(t)
	}
	if code, text := run("1", shared); code != 0 || !strings.Contains(text, "quota=100000") {
		t.Fatalf("budget 1: exit %d, want a pass at quota 100000", code)
	}
	code, text := run("2", shared)
	wantAll(t, text, "quota=200000")
	if code != 1 || strings.Contains(text, "cached") {
		t.Errorf("budget 2 on the budget-1 cache: exit %d; want the step run again and failed, as on a fresh cache", code)
	}
	if code, _ := run("2", t.TempDir()); code != 1 {
		t.Errorf("budget 2 on a fresh cache: exit %d, want 1", code)
	}
}

// effectiveVariable names the shared cache of TestCPUBudgetKeysTheEffectiveBudget's
// helper; the helper runs only when it is set.
const effectiveVariable = "GATE_EFFECTIVE_CACHE"

// The step receipt key holds the effective budget, not the requested one:
// with ERGO_GATE_CPUS=8 fixed, a gate in a cgroup with CPUQuota=200% (budget
// 2) records a pass that a gate under CPUQuota=400% (budget 4) on the same
// cache does not replay; it runs the step, as a gate on a fresh cache does
// (the round-2 verifier's TestR2EffectiveBudgetKey). The step passes only at
// -p=2 and declares only sh, so only the step key's budget line keeps the
// pass from crossing.
func TestCPUBudgetKeysTheEffectiveBudget(t *testing.T) {
	if _, err := exec.LookPath("systemd-run"); err != nil {
		t.Skip("no systemd-run")
	}
	if usableCPUsOnce() < 4 {
		t.Skip("fewer than 4 usable CPUs: no budget 4")
	}
	slice := testSlice(t)
	t.Setenv(cpuVariable, "8")
	t.Setenv("GOMAXPROCS", "1")
	t.Setenv("GOFLAGS", "")
	root, shared := fixture(t), t.TempDir()
	run := func(quota, cache string) (int, int, string) {
		t.Helper()
		command := exec.Command("systemd-run", "--user", "--scope", "--quiet", "--collect", "--slice="+slice, "-p", "CPUQuota="+quota, "--",
			os.Args[0], "-test.run=^TestCPUBudgetEffectiveHelper$", "-test.count=1", "-test.v")
		command.Env = append(scopeEnvironment(""), effectiveVariable+"="+cache, "GATE_EFFECTIVE_ROOT="+root)
		out, err := command.CombinedOutput()
		text := string(out)
		var code, budget int
		_, verdict, _ := strings.Cut(text, "effective budget=")
		if _, scanErr := fmt.Sscanf(verdict, "%d verdict=%d", &budget, &code); scanErr != nil {
			t.Fatalf("quota %s: %v, no verdict (%v):\n%s", quota, err, scanErr, text)
		}
		return budget, code, text
	}
	if budget, code, text := run("200%", shared); budget != 2 || code != 0 {
		t.Fatalf("quota 200%%: budget %d exit %d, want budget 2 and a pass:\n%s", budget, code, text)
	}
	budget, reused, text := run("400%", shared)
	if budget != 4 {
		t.Fatalf("quota 400%%: budget %d, want 4:\n%s", budget, text)
	}
	_, fresh, _ := run("400%", t.TempDir())
	if reused != fresh || fresh != 1 || strings.Contains(text, "cached") {
		t.Errorf("quota 400%% on the 200%% cache: exit %d, on a fresh cache %d; want both 1, the step run again:\n%s", reused, fresh, text)
	}
}

// TestCPUBudgetEffectiveHelper is the gate TestCPUBudgetKeysTheEffectiveBudget
// runs in each quota.
func TestCPUBudgetEffectiveHelper(t *testing.T) {
	cache := os.Getenv(effectiveVariable)
	if cache == "" {
		t.Skip("a helper of TestCPUBudgetKeysTheEffectiveBudget")
	}
	g := wholeFixture(t, os.Getenv("GATE_EFFECTIVE_ROOT"), []*Step{{
		Name:   "effective-sensitive",
		Argv:   []string{"sh", "-c", `p=${GOFLAGS##*-p=}; p=${p%% *}; echo "p=$p"; test "$p" = 2`},
		Inputs: &Inputs{Paths: []string{"src/"}, Tools: []string{"sh"}},
	}})
	g.cacheDir = cache
	code, out := runWhole(t, g)
	fmt.Printf("%s\neffective budget=%d verdict=%d\n", out, stepCPUs(), code)
}

// A gate in a scope without its CPU quota refuses to run a step, and says
// that the scope does not enforce the CPU budget, not that it lacks memory
// protection.
func TestCPUBudgetRefusesAScopeWithoutTheQuota(t *testing.T) {
	slice := testSlice(t)
	setBudgetCaseTemp(t)
	t.Setenv(stepMemoryVariable, "512M")
	t.Setenv(gateMemoryVariable, "1G")
	t.Setenv(hostMemoryVariable, "4G")
	t.Setenv(cpuVariable, "1")
	budget, err := currentBudget()
	if err != nil {
		t.Fatal(err)
	}
	startableScope(t, slice, budget)
	h := newScopeHelper(t, slice, cpuCase.name, budget)
	quota := slices.Index(h.command.Args, "CPUQuota=100%")
	if quota < 1 {
		t.Fatalf("no CPUQuota=100%% in %q", h.command.Args)
	}
	h.command.Args = slices.Delete(h.command.Args, quota-1, quota+1)
	h.command.Run()
	code, text := h.result(t)
	wantAll(t, text, "gate: cpu budget: systemd scope ", " does not enforce the CPU budget: ", `cpu.max is "max 100000", not 1 CPUs (100000 100000)`)
	if code != 2 || strings.Contains(text, "OOM protection") || strings.Contains(text, "== probe") {
		t.Errorf("exit %d; want 2, a refusal without the memory wording, and no step", code)
	}
}

// In the gate's systemd scope the kernel enforces the CPU budget: the scope
// and each step's cgroup have cpu.max at the budget, and a step's Go
// process runs with GOMAXPROCS at the budget, and a Go child with no
// GOMAXPROCS reads its step's cpu.max (Go's runtime reads only its own
// cgroup's, and never sets GOMAXPROCS below 2).
func TestCPUBudgetSystemdScope(t *testing.T) {
	slice := testSlice(t)
	setBudgetCaseTemp(t)
	t.Setenv(stepMemoryVariable, "512M")
	t.Setenv(gateMemoryVariable, "1G")
	t.Setenv(hostMemoryVariable, "4G")
	t.Setenv(cpuVariable, "1")
	budget, err := currentBudget()
	if err != nil {
		t.Fatal(err)
	}
	startableScope(t, slice, budget)
	h := newScopeHelper(t, slice, cpuCase.name, budget)
	h.command.Run()
	code, text := h.result(t)
	child := min(2, runtime.NumCPU())
	wantAll(t, text,
		"gate: cpu budget: 1 CPUs, systemd scope CPUQuota=100%, cpu.max 100000 100000 per step\n",
		`cpu-probe leaf="100000 100000" scope="100000 100000" gomaxprocs=1`+"\n",
		fmt.Sprintf("cpu-probe child gomaxprocs=%d\n", child),
		"== probe: ok")
	if code != 0 {
		t.Errorf("gate exit %d, want 0", code)
	}
}
