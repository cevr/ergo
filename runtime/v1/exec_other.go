//go:build !linux && !darwin && !dragonfly && !freebsd && !netbsd && !openbsd

package v1

import (
	"errors"
	"runtime"
	"syscall"
)

// execUnsupported is why Run and Start refuse on this platform, the Reason
// of the exec.Failed they return before anything starts. A program in the
// browser or under WASI has no processes to start; on Windows a stop must
// reach the program's own children (a job object), which the runtime does
// not do yet, and a Run that left them running would break exec's contract.
const execUnsupported = "starting a program is not supported on " + runtime.GOOS + "/" + runtime.GOARCH

var errExecUnsupported = errors.New(execUnsupported)

// The helpers below are unreachable (Run and Start refuse first); they
// fail rather than do nothing, so a path that reached one would surface.

func execGroupAttr() *syscall.SysProcAttr { return nil }

func execKill(int, syscall.Signal) error { return errExecUnsupported }

type execExit struct{}

func execWatchExit() (execExit, error) { return execExit{}, errExecUnsupported }

func (execExit) await(int) {}

func (execExit) close() {}
