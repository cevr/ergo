//go:build linux || darwin || dragonfly || freebsd || netbsd || openbsd

package exec

import (
	"os"
	"os/exec"
	"slices"
	"syscall"

	v1 "ergo.local/prototype/runtime/v1"
)

// ExecReplace is the body of exec.Replace: this process becomes the
// program name, found as Run finds it (Go's exec.LookPath), with args
// after it, the host process's environment and env's entries over it, in
// the working directory dir ("" keeps this one's). It keeps this
// process's id, its standard streams and its process group, so the shell
// that waits for this process waits for the program and a terminal's
// interrupt reaches the program. It flushes standard output first, as an
// exit does. It returns only with a failure; then this process is as it
// was, its working directory included.
//
//ergo:host
//ergo:wait none
func ExecReplace(name string, args []string, env map[string]string, dir string) error {
	if name == "" {
		return ExecNotFound{Name: name}
	}
	path, err := exec.LookPath(name)
	if err != nil {
		return execErr(name, err)
	}
	environ := os.Environ()
	if dir != "" {
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			reason := "not a directory"
			if err != nil {
				reason = fsReason(err)
			}
			return ExecFailed{Name: name, Reason: "dir " + dir + ": " + reason}
		}
		// As os/exec does for a command with a Dir and no Env of its own:
		// PWD names the directory the program runs in.
		environ = append(environ, "PWD="+dir)
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		environ = append(environ, k+"="+env[k])
	}
	// A later entry of one key wins over an earlier one (Go's
	// dedupEnv, which os/exec applies too).
	environ = dedupEnv(environ)
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
	err = syscall.Exec(path, append([]string{name}, args...), environ)
	if dir != "" {
		_ = os.Chdir(back)
	}
	return execErr(name, err)
}

// dedupEnv keeps the last entry of each key, in the order of those last
// entries' places.
func dedupEnv(environ []string) []string {
	last := map[string]int{}
	for i, kv := range environ {
		last[envKey(kv)] = i
	}
	out := environ[:0:0]
	for i, kv := range environ {
		if last[envKey(kv)] == i {
			out = append(out, kv)
		}
	}
	return out
}

// envKey is the key of an environment entry "key=value".
func envKey(kv string) string {
	for i := 0; i < len(kv); i++ {
		if kv[i] == '=' && i > 0 {
			return kv[:i]
		}
	}
	return kv
}
