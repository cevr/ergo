//go:build !linux && !darwin && !dragonfly && !freebsd && !netbsd && !openbsd

package exec

import (
	"os"
	"time"

	"ergo.local/prototype/runtime/rt"
)

var errPTYUnsupported = ExecUnsupported{Reason: "PTY, FD reattachment, and detached wait are not supported on this OS"}

func osFileFd(f *os.File) int {
	if f == nil {
		return -1
	}
	return int(f.Fd())
}

func osDupFileRaw(*os.File, string) (int, error) {
	return -1, errPTYUnsupported
}

func ptyGetSizeOS(*os.File) (ExecWinsize, error) {
	return ExecWinsize{}, errPTYUnsupported
}

func ptySetSizeOS(*os.File, ExecWinsize) error {
	return errPTYUnsupported
}

func readFileInterruptible(*rt.Task, *os.File, <-chan struct{}, string, bool) (string, error) {
	return "", errPTYUnsupported
}

func execOpenPTYOS() (*PTY, error) {
	return nil, errPTYUnsupported
}

func execStartWithPTYOS(*rt.Task, string, []string, map[string]string, string, string, time.Duration, *PTY) (*Running, error) {
	return nil, errPTYUnsupported
}

func execSpawnWithPTYOS(*rt.Task, string, []string, map[string]string, string, string, *PTY) (ExecDetached, error) {
	return ExecDetached{}, errPTYUnsupported
}

func execOpenPipeOS() (*Pipe, *Pipe, error) {
	return nil, nil, errPTYUnsupported
}

func execReattachPTYOS(int) (*PTY, error) {
	return nil, errPTYUnsupported
}

func execReattachPipeOS(int) (*Pipe, error) {
	return nil, errPTYUnsupported
}

func execPollDetachedOS(int, int64) (int, int, int, bool, string, error) {
	return 0, 0, 0, false, "", errPTYUnsupported
}

func execWaitDetachedOS(*rt.Task, int, int64) (int, int, int, bool, string, error) {
	return 0, 0, 0, false, "", errPTYUnsupported
}
