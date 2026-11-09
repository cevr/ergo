//go:build !linux

package proclimit

import "os/exec"

// start only starts cmd here: see the package comment.
func start(cmd *exec.Cmd, _ uint64) error { return cmd.Start() }
