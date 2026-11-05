package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
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
