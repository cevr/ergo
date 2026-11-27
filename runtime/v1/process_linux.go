//go:build linux

package v1

import (
	"fmt"
	"os"
	"os/exec"
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
			limitBytes, err := strconv.ParseInt(rawBytes, 10, 64)
			if err != nil || limitBytes <= 0 {
				fmt.Fprintf(os.Stderr, "ergo process wrapper: invalid memory limit %q\n", rawBytes)
				os.Exit(126)
			}
			if err := applyProcessBytesLimit(limitBytes); err != nil {
				fmt.Fprintf(os.Stderr, "ergo process wrapper: %v\n", err)
				os.Exit(126)
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
			if err := applyProcessBytesLimit(limitBytes); err != nil {
				fmt.Fprintf(os.Stderr, "ergo process limit: %v\n", err)
				os.Exit(126)
			}
		}
	}
	if len(os.Args) > 1 && os.Args[1] == processWorkerFlag {
		runProcessWorkerMain()
		os.Exit(0)
	}
}

func applyProcessBytesLimit(limitBytes int64) error {
	var cur syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_DATA, &cur); err != nil {
		return fmt.Errorf("getrlimit(RLIMIT_DATA): %w", err)
	}
	want := uint64(limitBytes)
	if cur.Max != ^uint64(0) && want > cur.Max {
		want = cur.Max
	}
	if err := syscall.Setrlimit(syscall.RLIMIT_DATA, &syscall.Rlimit{Cur: want, Max: want}); err != nil {
		return fmt.Errorf("setrlimit(RLIMIT_DATA, %d): %w", want, err)
	}
	_ = os.WriteFile("/proc/self/oom_score_adj", []byte("1000"), 0o644)
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
	exe, err := os.Executable()
	if err == nil && exe != "" {
		return exe, nil
	}
	if _, statErr := os.Stat("/proc/self/exe"); statErr == nil {
		return "/proc/self/exe", nil
	}
	if err != nil {
		return "", err
	}
	return "", fmt.Errorf("cannot locate current executable")
}
