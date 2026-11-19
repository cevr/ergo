//go:build linux

package irspike

import (
	"errors"
	"os/exec"
	"time"

	"ergo.local/prototype/internal/opcount"
)

// Cost is the counted work of one command and its descendants.
type Cost struct {
	// Instructions are the user-space instructions retired by the
	// command's process tree (internal/opcount), or -1 when there is no
	// whole count: the host exposes no hardware counter, or the kernel
	// multiplexed it (a short count is no count).
	Instructions int64 `json:"instructions"`
	// UserCPU and SysCPU are the rusage CPU times of the process tree
	// (the waited-for descendants included).
	UserCPU time.Duration `json:"user_cpu_ns"`
	SysCPU  time.Duration `json:"sys_cpu_ns"`
	// MaxRSS is the largest resident set of one process of the tree, KiB.
	MaxRSS int64 `json:"max_rss_kib"`
}

// Measure runs cmd and counts its process tree's work.
func Measure(cmd *exec.Cmd) (Cost, error) { return MeasureBounded(cmd, 0) }

// MeasureBounded is Measure with the program under proclimit's memory
// limit (0: none), which the kernel enforces. Its error is the start's or
// cmd.Wait's (an *exec.ExitError for a nonzero exit).
func MeasureBounded(cmd *exec.Cmd, limit uint64) (Cost, error) {
	r, err := opcount.Count(cmd, limit)
	if err != nil {
		return Cost{Instructions: -1}, err
	}
	c := Cost{Instructions: -1, UserCPU: r.UserCPU, SysCPU: r.SysCPU, MaxRSS: r.MaxRSSKiB}
	if id, _ := r.Refusal(); id == "" {
		c.Instructions = int64(r.Instructions)
	}
	if r.Wait == nil && r.Incomplete != "" {
		return c, errors.New(r.Incomplete)
	}
	return c, r.Wait
}
