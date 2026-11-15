//go:build !linux

package irspike

import (
	"os/exec"
	"syscall"
	"time"

	"ergo.local/prototype/internal/proclimit"
)

// Cost is the counted work of one command and its descendants. Off Linux
// no hardware counter is read: Instructions is -1.
type Cost struct {
	Instructions int64         `json:"instructions"`
	UserCPU      time.Duration `json:"user_cpu_ns"`
	SysCPU       time.Duration `json:"sys_cpu_ns"`
	MaxRSS       int64         `json:"max_rss_kib"`
}

// Measure runs cmd; see counter_linux.go.
func Measure(cmd *exec.Cmd) (Cost, error) { return MeasureBounded(cmd, 0) }

// CountSelf runs f; off Linux it counts nothing (-1).
func CountSelf(f func()) int64 {
	f()
	return -1
}

// MeasureBounded is Measure with the program under proclimit's memory
// limit (0: none).
func MeasureBounded(cmd *exec.Cmd, limit uint64) (Cost, error) {
	err := proclimit.Start(cmd, limit)
	if err == nil {
		err = cmd.Wait()
	}
	c := Cost{Instructions: -1}
	if cmd.ProcessState != nil {
		if ru, ok := cmd.ProcessState.SysUsage().(*syscall.Rusage); ok {
			c.UserCPU = time.Duration(ru.Utime.Nano())
			c.SysCPU = time.Duration(ru.Stime.Nano())
			c.MaxRSS = ru.Maxrss
		}
	}
	return c, err
}
