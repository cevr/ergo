package main

import (
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"sync"
)

// The CPU budget, beside the memory budget: the number of CPUs one gate may
// use. It bounds how many steps run at once (slots), how many shards a
// package splits into, the GOMAXPROCS of every step's Go processes (and so
// cmd/go's -p and a test binary's -parallel, whose defaults are GOMAXPROCS)
// and the gate's own GOMAXPROCS. On Linux in the gate's systemd scope the
// kernel also enforces it: the scope has CPUQuota=<budget*100>%, and each
// step's cgroup has cpu.max at the budget, so Go's runtime, which reads only
// the cpu.max of the cgroup a process is in, finds it there too.
const (
	// cpuVariable overrides the CPU budget, half of the usable CPUs.
	cpuVariable = "ERGO_GATE_CPUS"
	// scopeCPUsVariable passes, to the gate re-executed in its systemd
	// scope, the budget the gate decided before it entered the scope. The
	// gate reads it once and removes it from its environment (takeScope),
	// so the whole-gate key, which holds every ERGO_GATE_* variable, is the
	// same in and out of the scope. Inside, the scope's own quota would
	// halve the budget again.
	scopeCPUsVariable = "ERGO_GATE_SCOPE_CPUS"
	// maxCPUs is the largest budget the variable takes.
	maxCPUs = 1 << 16
	// cpuPeriod is the cpu.max period, in microseconds, the gate writes:
	// the kernel's and systemd's default.
	cpuPeriod = 100000
)

// scopeCPUs is the budget passed by scopeCPUsVariable, or 0 outside a gate
// scope.
var scopeCPUs int

// takeScope reads and removes the variables that the gate gives itself when
// it re-executes in its systemd scope: the scope's unit, and the CPU budget
// it decided outside the scope. It returns the unit, or "" outside a scope.
func takeScope() (string, error) {
	scope := os.Getenv(scopeVariable)
	cpus := os.Getenv(scopeCPUsVariable)
	os.Unsetenv(scopeVariable)
	os.Unsetenv(scopeCPUsVariable)
	scopeCPUs = 0
	if scope == "" || cpus == "" {
		return scope, nil
	}
	n, err := parseCPUs(cpus)
	if err != nil {
		return "", fmt.Errorf("%s: %w", scopeCPUsVariable, err)
	}
	scopeCPUs = n
	return scope, nil
}

// currentCPUs is the CPU budget: the budget the gate decided before it
// entered its scope, or else cpuBudgetFor the variable and the usable CPUs.
func currentCPUs() (int, error) {
	if scopeCPUs > 0 {
		return scopeCPUs, nil
	}
	return cpuBudgetFor(os.Getenv(cpuVariable), usableCPUsOnce())
}

// stepCPUs is currentCPUs, or 1 when the variable does not parse (the gate
// refuses to start then, so no step runs with it).
func stepCPUs() int {
	cpus, err := currentCPUs()
	if err != nil {
		return 1
	}
	return cpus
}

// cpuBudgetFor is the budget for the variable's value (empty when unset)
// on a host where the gate may run on usable CPUs: the value, or half of
// the usable CPUs and at least 2; never more than the usable CPUs, which
// is all the gate can use.
func cpuBudgetFor(value string, usable int) (int, error) {
	usable = max(1, usable)
	requested := max(2, usable/2)
	if value != "" {
		n, err := parseCPUs(value)
		if err != nil {
			return 0, fmt.Errorf("%s: %w", cpuVariable, err)
		}
		requested = n
	}
	return min(requested, usable), nil
}

// parseCPUs reads a CPU count: a positive decimal number.
func parseCPUs(value string) (int, error) {
	n, err := strconv.Atoi(value)
	if err != nil || n <= 0 || n > maxCPUs {
		return 0, fmt.Errorf("%q is not a positive whole number of CPUs such as 4 or 8", value)
	}
	return n, nil
}

var usableCPUsOnce = sync.OnceValue(readUsableCPUs)

// readUsableCPUs is the number of CPUs this process may run on: the CPUs of
// its affinity mask, lowered to the tightest cpu.max of its cgroup and every
// cgroup above it, rounded up.
func readUsableCPUs() int {
	return usableCPUs(affinityCPUs(), cgroupCPULimits())
}

// usableCPUs is the affinity count lowered to the tightest of limits, each
// a cgroup's cpu.max ("max <period>" or "<quota> <period>"), rounded up. A
// limit that does not parse bounds nothing.
func usableCPUs(affinity int, limits []string) int {
	usable := max(1, affinity)
	for _, limit := range limits {
		if cpus, ok := cpuMaxCPUs(limit); ok {
			usable = min(usable, cpus)
		}
	}
	return usable
}

// cpuMaxCPUs is the CPUs a cpu.max content allows, rounded up, and whether
// it sets a bound.
func cpuMaxCPUs(content string) (int, bool) {
	fields := strings.Fields(content)
	if len(fields) != 2 || fields[0] == "max" {
		return 0, false
	}
	quota, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil || quota <= 0 {
		return 0, false
	}
	period, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil || period <= 0 {
		return 0, false
	}
	return max(1, int(math.Ceil(float64(quota)/float64(period)))), true
}

// cpuMax is the cpu.max content that allows cpus CPUs.
func cpuMax(cpus int) string {
	return fmt.Sprintf("%d %d", int64(cpus)*cpuPeriod, cpuPeriod)
}

// cpuQuota is systemd's CPUQuota for cpus CPUs.
func cpuQuota(cpus int) string {
	return strconv.Itoa(cpus*100) + "%"
}

// cpuEnforcer is a limiter whose cgroups enforce the CPU budget.
type cpuEnforcer interface {
	// cpuNote says how, for the budget line.
	cpuNote() string
}

// describeCPUs is the gate's CPU budget line.
func describeCPUs(limit limiter, cpus int) string {
	note := "steps at once and GOMAXPROCS (no cgroup)"
	if enforcer, ok := limit.(cpuEnforcer); ok {
		note = enforcer.cpuNote()
	}
	return fmt.Sprintf("%d CPUs, %s", cpus, note)
}

// stepGOMAXPROCS is the GOMAXPROCS of every step's Go processes: the
// caller's when it is a whole number no larger than the budget, or else the
// budget. A child that does not read cgroups, or reads only its own leaf as
// Go's runtime does, still runs at most cpus threads of Go code at once.
func stepGOMAXPROCS(caller string, cpus int) string {
	if n, err := strconv.Atoi(caller); err == nil && n > 0 && n <= cpus {
		return caller
	}
	return strconv.Itoa(cpus)
}
