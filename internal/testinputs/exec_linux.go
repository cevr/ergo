//go:build linux && (amd64 || arm64)

package testinputs

import (
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"syscall"
)

// Exec runs a command under observation (ergo task exec): this binary,
// which links this package, starts again as a child that installs the
// filter, hands its listener over and executes the command in its place
// (runTarget). It returns when the command exits, with what its process
// tree depended on. Result.Reason is not empty when the
// observation is incomplete; the result must then not be reused.
//
// The command gets spec.Env and nothing else (no variable of this
// process), standard input from /dev/null (a pipe or a terminal is an
// input no path names), and spec.Dir as its working directory.
func Exec(spec ExecSpec) (ExecResult, error) {
	if len(spec.Argv) == 0 {
		return ExecResult{}, fmt.Errorf("no command")
	}
	executable, err := os.Executable()
	if err != nil {
		return ExecResult{}, err
	}
	command := exec.Command(executable)
	command.Args = spec.Argv
	command.Dir = spec.Dir
	for _, kv := range spec.Env {
		if !strings.HasPrefix(kv, "ERGO_TESTINPUTS_") {
			command.Env = append(command.Env, kv)
		}
	}
	command.Env = append(command.Env, targetVariable+"=1")
	command.Stdout, command.Stderr = spec.Stdout, spec.Stderr
	o := newObserver("/")
	o.target = true
	if err := o.run(command); err != nil {
		return ExecResult{}, err
	}
	if !o.armed.Load() {
		o.rec.refuse("the command never ran under the filter")
	}
	result := ExecResult{Exit: childExit(command.ProcessState), Reason: o.rec.reason()}
	for path, key := range o.rec.paths {
		if key&keyOpen != 0 {
			result.Inputs = append(result.Inputs, Input{path, SnapRead})
		}
		if key&keyStat != 0 {
			result.Inputs = append(result.Inputs, Input{path, SnapLookup})
		}
	}
	slices.SortFunc(result.Inputs, func(a, b Input) int {
		if order := strings.Compare(a.Path, b.Path); order != 0 {
			return order
		}
		return int(a.Kind) - int(b.Kind)
	})
	return result, nil
}

func childExit(state *os.ProcessState) int {
	if state == nil {
		return 1
	}
	if status, ok := state.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		return 128 + int(status.Signal())
	}
	return state.ExitCode()
}
