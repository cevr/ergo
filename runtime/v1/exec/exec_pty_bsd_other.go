//go:build dragonfly || freebsd || netbsd || openbsd

package exec

import (
	"os"
	"syscall"
)

func openOSPTY() (master, slave *os.File, slavePath string, err error) {
	return nil, nil, "", ExecUnsupported{Reason: "PTY allocation is not supported on this BSD OS"}
}

func execNoteExitFlags() uint32 {
	return syscall.NOTE_EXIT
}

func execKqueueExitStatus(uint32, int64) (int, int, bool) {
	return 0, 0, false
}
