package testinputs

import (
	"os"
	"os/exec"
	"strings"
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

// boxedVariable marks a recording test process that boxRecording has
// started in its box.
const boxedVariable = "ERGO_CLOSURE_RECORD_BOXED"

// boxRecording gives a recorded run the sweep's view: the module root
// read-only (S1) and no user namespace (S3). A run that already has it (in
// the sweep's own sandbox) stays as it is. Otherwise the test process
// executes itself again in bubblewrap, with the root bound read-only and
// user namespaces disabled, so a go test step needs no wrapping and a
// package whose tests do not record (no testinputs.Main) runs as before.
// It returns only when the run stays where it is, with "" when that view
// holds and else why not: the record's probes then say so, and the sweep
// runs the package itself. bwrap is tried on true first, so a host where
// it cannot box fails over to a rerun instead of failing the tests.
func boxRecording() string {
	_, boxed := os.LookupEnv(boxedVariable)
	os.Unsetenv(boxedVariable)
	root, err := moduleRoot()
	if err != nil {
		return err.Error()
	}
	readOnly, tree := probeTree(root)
	userns := probeUserNamespaces()
	if readOnly && strings.HasPrefix(userns, UserNamespacesRefused) {
		return ""
	}
	if boxed {
		return "the box did not hold: " + tree + "; user namespaces " + userns
	}
	bwrap, err := exec.LookPath("bwrap")
	if err != nil {
		return err.Error()
	}
	self, err := os.Executable()
	if err != nil {
		return err.Error()
	}
	box := []string{bwrap, "--unshare-user", "--disable-userns", "--die-with-parent", "--dev-bind", "/", "/", "--ro-bind", root, root, "--"}
	program, err := exec.LookPath("true")
	if err != nil {
		return err.Error()
	}
	if out, err := exec.Command(bwrap, append(box[1:], program)...).CombinedOutput(); err != nil {
		return "bwrap cannot box the run: " + err.Error() + ": " + strings.TrimSpace(string(out))
	}
	err = syscall.Exec(bwrap, append(append(box, self), os.Args[1:]...), append(os.Environ(), boxedVariable+"=1"))
	return "execute the run in bwrap: " + err.Error()
}
