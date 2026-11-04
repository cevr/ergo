package closure

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
)

// The sweep runs every test binary with the module checkout read-only. A
// recorded path inside the checkout then names, after the run, the object
// it named when the test read it: nothing in the checkout can change
// during the run, so the classifier resolves each path against the same
// tree the test saw. A test that tries to change the checkout fails with
// EROFS, and the sweep reports it as a closure violation. This is a rule of
// construction, as in Bazel's sandbox, not an inference from the log.
//
// The read-only view is a bind mount in an unprivileged user and mount
// namespace (Linux only). The sweep starts this test binary again as a
// helper in a new namespace (sandboxVariable names the directory to
// protect); the helper makes the directory read-only, drops its
// capabilities and runs the test binary by exec, so the tests and every
// process they start see the read-only checkout. When the namespace or the
// mount cannot be made, the helper exits with sandboxExit and the sweep
// refuses to run: it never runs a test binary with a writable checkout.

const (
	// sandboxVariable, in the helper, names the directory to make
	// read-only. The helper's arguments are the program to run and its
	// arguments.
	sandboxVariable = "ERGO_CLOSURE_SANDBOX"
	// sandboxExit is the helper's exit code when it cannot protect the
	// directory; its output then starts with sandboxMarker and the reason.
	sandboxExit   = 125
	sandboxMarker = "closure sandbox: "
)

// helperMain runs the helper modes of this test binary, from TestMain,
// before testinputs.Main. It returns only when the binary runs as a test.
func helperMain() {
	if root, ok := os.LookupEnv(sandboxVariable); ok {
		sandboxMain(root, os.Args[1:])
	}
	if op, ok := os.LookupEnv(childVariable); ok {
		os.Exit(childMain(op))
	}
}

// sandboxMain makes root read-only in this process's mount namespace and
// runs args by exec. It never returns.
func sandboxMain(root string, args []string) {
	fail := func(err error) {
		fmt.Fprintf(os.Stderr, "%s%v\n", sandboxMarker, err)
		os.Exit(sandboxExit)
	}
	if len(args) == 0 {
		fail(errors.New("no program to run"))
	}
	// The working directory was looked up before the mount, so it is still
	// the writable directory under it: look it up again by its name.
	wd, err := os.Getwd()
	if err != nil {
		fail(err)
	}
	if err := makeReadOnly(root); err != nil {
		fail(fmt.Errorf("make %s read-only: %w", root, err))
	}
	if err := os.Chdir(wd); err != nil {
		fail(err)
	}
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, sandboxVariable+"=") {
			env = append(env, kv)
		}
	}
	program, err := exec.LookPath(args[0])
	if err != nil {
		fail(err)
	}
	if err := dropCapabilities(); err != nil {
		fail(err)
	}
	// dropCapabilities locked this goroutine to its thread, so the exec
	// runs from the thread without ambient capabilities.
	fail(syscall.Exec(program, args, env))
}

// refusal is the sweep's refusal to run a test binary: the checkout could
// not be made read-only.
type refusal struct{ reason string }

func (r refusal) Error() string {
	return "the sweep refuses to run: it cannot make the checkout read-only: " + r.reason
}

// violation is a test package that tried to write into the checkout, which
// the sweep mounts read-only. Its tests must write to t.TempDir instead.
type violation struct {
	pkg   string
	lines []string // the output lines that report the refused writes
}

func (v violation) Error() string {
	return fmt.Sprintf("closure violation: %s writes into the checkout, which the sweep mounts read-only (write to t.TempDir instead):\n  %s", v.pkg, strings.Join(v.lines, "\n  "))
}

// runProtected runs binary with args from dir, with protect read-only, the
// environment env and its output in output. It returns a refusal when the
// checkout cannot be made read-only (the binary did not run), a violation
// when the binary failed and reports a write the mount refused, and the
// binary's own error otherwise.
func runProtected(protect, pkg, dir string, env []string, output string, binary string, args ...string) error {
	file, err := os.Create(output)
	if err != nil {
		return err
	}
	cmd, err := sandboxCommand(protect, binary, args...)
	if err != nil {
		file.Close()
		return refusal{err.Error()}
	}
	cmd.Dir = dir
	cmd.Env = append(env, sandboxVariable+"="+protect)
	cmd.Stdout, cmd.Stderr = file, file
	runErr := cmd.Run()
	file.Close()
	if runErr == nil {
		return nil
	}
	out, _ := os.ReadFile(output)
	var exit *exec.ExitError
	switch {
	case !errors.As(runErr, &exit):
		// The helper never started: no namespace.
		return refusal{runErr.Error()}
	case exit.ExitCode() == sandboxExit && bytes.HasPrefix(out, []byte(sandboxMarker)):
		return refusal{strings.TrimSpace(strings.TrimPrefix(string(out), sandboxMarker))}
	}
	var refused []string
	for line := range strings.SplitSeq(string(out), "\n") {
		if strings.Contains(line, syscall.EROFS.Error()) {
			refused = append(refused, strings.TrimSpace(line))
		}
	}
	if len(refused) > 0 {
		return violation{pkg, refused[:min(5, len(refused))]}
	}
	tail := out
	if len(tail) > 4000 {
		tail = tail[len(tail)-4000:]
	}
	return fmt.Errorf("the tests failed (%v), so the inputs they record are incomplete; output %s ends:\n%s", runErr, output, tail)
}

// needSandbox skips a test that makes a namespace when cmd/go caches its
// result: testinputs refuses to cache a run that makes one, which would
// make this package rerun on every gate. The sweep and these tests run
// with -count=1, unobserved.
func needSandbox(t *testing.T) {
	t.Helper()
	if f := flag.Lookup("test.testlogfile"); f != nil && f.Value.String() != "" {
		t.Skip("cmd/go caches this run, and a namespace would make it uncacheable (testinputs); run with -count=1")
	}
	if err := sandboxSupported(); err != nil {
		t.Skip(err)
	}
}
