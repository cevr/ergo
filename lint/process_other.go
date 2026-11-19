//go:build !unix

package lint

import (
	"os"
	"syscall"
)

// Without process groups only the pack process itself can be killed.
func processGroupAttr() *syscall.SysProcAttr { return nil }

func killProcessGroup(process *os.Process) { process.Kill() }
