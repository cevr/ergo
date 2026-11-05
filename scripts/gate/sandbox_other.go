//go:build !linux

package main

import (
	"errors"
	"runtime"
)

// startSandbox: the merge tier's sandbox is bubblewrap in Linux
// namespaces; elsewhere the merge tier fails closed.
func startSandbox(sandboxPlan) (enclosure, error) {
	return nil, errors.New("no sandbox on " + runtime.GOOS + "; the merge tier runs only on Linux, in bubblewrap")
}
