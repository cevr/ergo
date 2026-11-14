// Package opcount counts the work of a program run: the user-space
// instructions of its whole process tree (the repository's one
// perf_event_open site, counter_linux.go), its rusage, and whether it
// finished. scripts/opcount builds receipts on it (bench.v1, receipt.go),
// internal/experimental/irspike and cmd/ergo's counted guards measure with
// it, and scripts/examples reads a program's workload file with it
// (workload.go).
//
// A count is a number only when it is whole. Count refuses, with a typed
// Refusal and no number:
//
//   - NoCounter: the host has no counter of user-space instructions (no PMU
//     in the VM, perf_event_paranoid above 2, or not Linux), or the counter
//     never ran;
//   - Multiplexed: the kernel shared the hardware counter with other events
//     and ran it for part of the run only, so the count is short (the
//     kernel's enabled time is above its running time); a scaled estimate
//     is not a count;
//   - RunIncomplete: the program exited with a status other than 0, was
//     killed by a signal, or left a process of its group running. A run
//     that failed is not a fast run (run-dir check-cpu.sh's `|| true` read
//     a failed `ergo check` as a fast one).
package opcount

import (
	"fmt"
	"time"
)

// Refusal is the typed reason a run or a series has no number. The ids are
// bench.v1's (receipt.go).
type Refusal string

// The refusals.
const (
	NoCounter     Refusal = "NoCounter"
	Multiplexed   Refusal = "Multiplexed"
	RunIncomplete Refusal = "RunIncomplete"
	NotEquivalent Refusal = "NotEquivalent"
	NoMetric      Refusal = "NoMetric"
)

// Run is what Count measured of one run of a program.
type Run struct {
	// Instructions are the user-space instructions retired by the
	// program's process tree, from its exec to its exit. They are a count
	// only when Uncounted is empty.
	Instructions uint64
	// Uncounted is NoCounter or Multiplexed when Instructions is no count,
	// with Why.
	Uncounted Refusal
	Why       string
	// Incomplete says why the run did not finish ("" when it exited 0 and
	// left no process): the run is then RunIncomplete, whatever it counted.
	Incomplete string
	// UserCPU, SysCPU and MaxRSSKiB are wait4's rusage of the program and
	// the descendants it waited for.
	UserCPU, SysCPU time.Duration
	MaxRSSKiB       int64
	// Wait is the error cmd.Wait returned: an *exec.ExitError for a
	// nonzero exit or a signal.
	Wait error
}

// Refusal is the run's refusal: RunIncomplete first, then the counter's,
// or "" when the run finished and its count is whole.
func (r Run) Refusal() (Refusal, string) {
	if r.Incomplete != "" {
		return RunIncomplete, r.Incomplete
	}
	return r.Uncounted, r.Why
}

// decode reads the counter's three words (read_format TOTAL_TIME_ENABLED |
// TOTAL_TIME_RUNNING: value, time enabled, time running, each summed over
// the process tree when its processes exit) as a whole count or a
// refusal. A counter that was never enabled counted nothing (NoCounter);
// one that ran for less time than it was enabled was multiplexed, and its
// value is short.
func decode(value, enabled, running uint64) (uint64, Refusal, string) {
	switch {
	case enabled == 0:
		return 0, NoCounter, "the counter was never enabled: the program was not exec'd under it"
	case running < enabled:
		return 0, Multiplexed, fmt.Sprintf("the counter ran %d of %d ns enabled: the kernel multiplexed it, so the count is short", running, enabled)
	}
	return value, "", ""
}
