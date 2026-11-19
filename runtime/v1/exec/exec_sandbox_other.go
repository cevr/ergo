//go:build !linux

package exec

import "os/exec"

// On a host other than Linux a command of exec.NewTestHost's folder runs
// with the path check only (docs/spec/rows.md, Test's ambient 5; TH1b):
// the paths given to exec are confined, the program's own path choices
// are not. Darwin's sandbox-exec is deprecated and its profile language
// undocumented, so it is not used.

func execSandboxStart(cmd *exec.Cmd, root string) (*execChild, error) { return execHostStart(cmd) }

// execSandboxEnd has no helper to end.
func execSandboxEnd(root string) {}

// execSandboxError is never made here: no sandbox can fail.
type execSandboxError struct{ err error }

func (e execSandboxError) Error() string { return e.err.Error() }

func sandboxReason(err error) string { return err.Error() }
