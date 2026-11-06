//go:build !linux

package closure

import (
	"errors"
	"os/exec"
	"runtime"
	"time"
)

// errUnsupported: only Linux has the unprivileged user and mount
// namespaces that make the file system read-only for the tests, and the
// sweep does not run with a writable checkout.
var errUnsupported = errors.New("unsupported on " + runtime.GOOS + ": the closure sweep needs Linux user and mount namespaces to make the file system read-only")

func sandboxSupported() error { return errUnsupported }

func sandboxCommand(string, ...string) (*exec.Cmd, error) { return nil, errUnsupported }

func runWithin(*exec.Cmd, time.Duration) (bool, error) { return false, errUnsupported }

func openProcess(int) (int, bool, error) { return -1, false, errUnsupported }

func readableWithin(int, time.Duration) (bool, error) { return false, errUnsupported }

func makeSandbox(sandbox) error { return errUnsupported }

func checkSandbox(sandbox) error { return errUnsupported }

func dropPrivileges() error { return errUnsupported }

func checkPrivileges(sandbox, string) error { return errUnsupported }

func readOnly(string) error { return errUnsupported }

func bindMount(string, string) error { return errUnsupported }

func reachedBy(string, bool) (string, [2]uint64, error) { return "", [2]uint64{}, errUnsupported }

func identity(string) ([2]uint64, error) { return [2]uint64{}, errUnsupported }
