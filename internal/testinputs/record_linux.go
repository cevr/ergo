package testinputs

import (
	"os/exec"
	"syscall"
)

// UserNamespacesRefused begins Record.UserNamespaces when the probe could
// not make a user namespace.
const UserNamespacesRefused = "refused: "

// probeUserNamespaces starts true in a new user namespace. The sweep's
// sandbox sets max_user_namespaces to 0 and bwrap --disable-userns refuses
// it otherwise, so the probe asks the kernel, not a setting.
func probeUserNamespaces() string {
	program, err := exec.LookPath("true")
	if err != nil {
		return "unknown: " + err.Error()
	}
	cmd := exec.Command(program)
	cmd.SysProcAttr = &syscall.SysProcAttr{Cloneflags: syscall.CLONE_NEWUSER}
	if err := cmd.Run(); err != nil {
		return UserNamespacesRefused + err.Error()
	}
	return "allowed"
}
