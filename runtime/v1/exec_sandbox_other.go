//go:build !linux

package v1

import "os/exec"

// On a host other than Linux a command of exec.NewTestHost's folder runs
// with the path check only (docs/spec/rows.md, Test's ambient 5; TH1b):
// the paths given to exec are confined, the program's own path choices
// are not. Darwin's sandbox-exec is deprecated and its profile language
// undocumented, so it is not used.

type execSandboxed struct{}

func execSandbox(cmd *exec.Cmd, root string) (execSandboxed, error) { return execSandboxed{}, nil }

func (execSandboxed) on() bool { return false }

func (execSandboxed) abandon() {}

func (execSandboxed) started() error { return nil }

func sandboxReason(err error) string { return err.Error() }
