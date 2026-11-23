//go:build linux || darwin || dragonfly || freebsd || netbsd || openbsd

package exec

import (
	"os"
	"syscall"

	v1 "ergo.local/prototype/runtime/v1"
)

// ExecReplace is the body of exec.Replace: this process becomes the
// program name, resolved as Run resolves it, with args after it, exactly
// env as its environment, and dir as its working directory ("" keeps
// this one's). It keeps this
// process's id, its standard streams and its process group, so the shell
// that waits for this process waits for the program and a terminal's
// interrupt reaches the program. It flushes standard output first, as an
// exit does. It returns only with a failure; then this process is as it
// was, its working directory included.
//
//ergo:host
//ergo:wait none
func ExecReplace(name string, args []string, env map[string]string, dir string) error {
	cmd, err := ExecCommand(name, args, env, dir, "")
	if err != nil {
		return err
	}
	var back string
	if dir != "" {
		if back, err = os.Getwd(); err != nil {
			return ExecFailed{Name: name, Reason: "getwd: " + fsReason(err)}
		}
		if err := os.Chdir(dir); err != nil {
			return ExecFailed{Name: name, Reason: "dir " + dir + ": " + fsReason(err)}
		}
	}
	v1.FlushStdout()
	err = syscall.Exec(cmd.Path, cmd.Args, cmd.Env)
	if dir != "" {
		_ = os.Chdir(back)
	}
	return execErr(name, err)
}
