//go:build !linux

package irspike

import (
	"os/exec"

	"ergo.local/prototype/internal/proclimit"
)

// Measure runs cmd. Outside Linux there is no perf_event_open, so the
// instructions are -1 (no hardware counter), as on a Linux host that
// exposes none; the CPU times come from the process state.
func Measure(cmd *exec.Cmd) (Cost, error) { return MeasureBounded(cmd, 0) }

// MeasureBounded is Measure with the program under proclimit's memory
// limit (0: none), where proclimit can apply one.
func MeasureBounded(cmd *exec.Cmd, limit uint64) (Cost, error) {
	err := proclimit.Start(cmd, limit)
	if err == nil {
		err = cmd.Wait()
	}
	c := Cost{Instructions: -1}
	if cmd.ProcessState != nil {
		c.UserCPU = cmd.ProcessState.UserTime()
		c.SysCPU = cmd.ProcessState.SystemTime()
	}
	return c, err
}
