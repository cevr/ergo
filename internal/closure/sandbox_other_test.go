//go:build !linux

package closure

import (
	"errors"
	"os/exec"
	"runtime"
)

// errUnsupported: only Linux has the unprivileged user and mount
// namespaces that make the checkout read-only, and the sweep does not run
// with a writable checkout.
var errUnsupported = errors.New("unsupported on " + runtime.GOOS + ": the closure sweep needs Linux user and mount namespaces to make the checkout read-only")

func sandboxSupported() error { return errUnsupported }

func sandboxCommand(string, string, ...string) (*exec.Cmd, error) { return nil, errUnsupported }

func makeReadOnly(string) error { return errUnsupported }

func dropCapabilities() error { return errUnsupported }

func readOnly(string) error { return errUnsupported }

func reachedBy(string, bool) (string, error) { return "", errUnsupported }
