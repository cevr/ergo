//go:build darwin || dragonfly || freebsd || netbsd || openbsd

package v1

import (
	"fmt"
	"os"
	"os/exec"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
)

const (
	processWrapEnv   = "__ERGO_PROCESS_WRAP"
	processTargetEnv = "__ERGO_PROCESS_TARGET"
	processBytesEnv  = "__ERGO_PROCESS_BYTES"
)

func init() {
	if os.Getenv(processWrapEnv) == "1" {
		target := os.Getenv(processTargetEnv)
		rawBytes := os.Getenv(processBytesEnv)
		_ = os.Unsetenv(processWrapEnv)
		_ = os.Unsetenv(processTargetEnv)
		_ = os.Unsetenv(processBytesEnv)
		if target == "" {
			fmt.Fprintln(os.Stderr, "ergo process wrapper: missing target binary")
			os.Exit(127)
		}
		if rawBytes != "" {
			if limitBytes, err := strconv.ParseInt(rawBytes, 10, 64); err == nil && limitBytes > 0 {
				_ = applyProcessBytesLimit(limitBytes)
			}
		}
		resolved := target
		if !strings.Contains(target, "/") {
			var err error
			resolved, err = exec.LookPath(target)
			if err != nil {
				fmt.Fprintf(os.Stderr, "ergo process wrapper: %v\n", err)
				os.Exit(127)
			}
		}
		argv := append([]string{target}, os.Args[1:]...)
		envSlice, err := buildChildEnv(OSEnviron(), "", 0)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ergo process wrapper: %v\n", err)
			os.Exit(126)
		}
		if err := syscall.Exec(resolved, argv, envSlice); err != nil {
			fmt.Fprintf(os.Stderr, "ergo process wrapper: exec %s: %v\n", resolved, err)
			os.Exit(127)
		}
		os.Exit(127)
	}
	if rawBytes := os.Getenv(processBytesEnv); rawBytes != "" {
		_ = os.Unsetenv(processBytesEnv)
		if limitBytes, err := strconv.ParseInt(rawBytes, 10, 64); err == nil && limitBytes > 0 {
			_ = applyProcessBytesLimit(limitBytes)
		}
	}
	if len(os.Args) > 1 && os.Args[1] == processWorkerFlag {
		runProcessWorkerMain()
		os.Exit(0)
	}
}

func applyProcessBytesLimit(limitBytes int64) error {
	if limitBytes > 0 {
		debug.SetMemoryLimit(limitBytes)
	}
	return nil
}

func processGroupAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}

func signalProcessGroup(pgid int, sig syscall.Signal) error {
	if pgid <= 0 {
		return nil
	}
	return syscall.Kill(-pgid, sig)
}

func processWrapperExecutable() (string, error) {
	return os.Executable()
}
