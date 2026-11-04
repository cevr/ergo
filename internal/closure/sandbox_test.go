package closure

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// The sweep runs every test binary in a sandbox built as Bazel's
// linux-sandbox builds one: the whole file system is read-only, and only
// scratch directories made fresh and empty for the run are writable. A
// recorded path inside the checkout then names, after the run, the object
// it named when the test read it: nothing in the checkout or above it can
// change during the run, by any name, so the classifier resolves each path
// against the tree the test saw. This is a rule of construction, not an
// inference from the log:
//
//   - S1: every mount of the sandbox's mount namespace is read-only,
//     recursively from the root of the file system: the checkout, each of
//     its ancestors, and any bind mount of it made before the run.
//   - S2: the only writable places are the run's scratch directories
//     (TMPDIR, GOTMPDIR, GOCACHE and the test log's directory), each a
//     mount of its own, outside the checkout and not an ancestor of it.
//     Linux refuses a hard link or a rename across mounts (EXDEV), so a
//     scratch directory cannot gain a name of a checkout file.
//   - S3: the tests hold no capability and cannot gain one (no_new_privs,
//     an empty bounding set, no new user namespace), so they cannot
//     remount, unmount or mount anything.
//   - S4: the sandbox checks S1 to S3 as the tests see it before they run,
//     and refuses the run, saying which part failed, when it cannot make or
//     verify one. It also refuses when the checkout has another name (a
//     bind mount of it, a mount inside it, a hard link of one of its files
//     outside it): a read by that name would not be classified.
//
// The sweep starts this test binary again as a helper in a new user and
// mount namespace (sandboxVariable holds the sandbox); the helper builds
// and checks the sandbox, drops its privileges and runs the test binary by
// exec, so the tests and every process they start run in it. A test that
// tries to change anything outside its scratch directories fails with
// EROFS, and the sweep reports it as a closure violation.

const (
	// sandboxVariable, in the helper, holds the sandbox as JSON. The
	// helper's arguments are the program to run and its arguments.
	sandboxVariable = "ERGO_CLOSURE_SANDBOX"
	// sandboxExit is the helper's exit code when it cannot build or verify
	// the sandbox; its output then starts with sandboxMarker and the reason.
	sandboxExit   = 125
	sandboxMarker = "closure sandbox: "
)

// sandbox is what one protected run needs: the checkout, by its physical
// path, and the scratch directories that stay writable.
type sandbox struct {
	Root    string
	Scratch []string
}

// helperMain runs the helper modes of this test binary, from TestMain,
// before testinputs.Main. It returns only when the binary runs as a test.
func helperMain() {
	if spec, ok := os.LookupEnv(sandboxVariable); ok {
		sandboxMain(spec, os.Args[1:])
	}
	if op, ok := os.LookupEnv(childVariable); ok {
		os.Exit(childMain(op))
	}
}

// sandboxMain builds the sandbox in this process's namespaces, checks it,
// drops this process's privileges and runs args by exec. It never returns.
func sandboxMain(spec string, args []string) {
	fail := func(err error) {
		fmt.Fprintf(os.Stderr, "%s%v\n", sandboxMarker, err)
		os.Exit(sandboxExit)
	}
	var sb sandbox
	if err := json.Unmarshal([]byte(spec), &sb); err != nil {
		fail(fmt.Errorf("%s: %w", sandboxVariable, err))
	}
	if len(args) == 0 {
		fail(errors.New("no program to run"))
	}
	wd, err := os.Getwd()
	if err != nil {
		fail(err)
	}
	if err := makeSandbox(sb); err != nil {
		fail(fmt.Errorf("make the sandbox: %w", err))
	}
	if err := checkSandbox(sb); err != nil {
		fail(fmt.Errorf("check the sandbox: %w", err))
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
	if err := dropPrivileges(); err != nil {
		fail(err)
	}
	// dropPrivileges locked this goroutine to its thread, so the exec runs
	// from the thread without capabilities.
	fail(syscall.Exec(program, args, env))
}

// refusal is the sweep's refusal to run a test binary: the sandbox could
// not be built or verified. The binary did not run.
type refusal struct{ reason string }

func (r refusal) Error() string {
	return "the sweep refuses to run: " + r.reason
}

// violation is a test package that tried to change the file system outside
// its scratch directories, which the sandbox makes read-only. Its tests
// must write to t.TempDir instead.
type violation struct {
	pkg   string
	lines []string // the output lines that report the refused writes
}

func (v violation) Error() string {
	return fmt.Sprintf("closure violation: %s writes outside its scratch directories, which the sweep makes read-only (write to t.TempDir instead):\n  %s", v.pkg, strings.Join(v.lines, "\n  "))
}

// runProtected runs binary with args from dir in the sandbox sb, with the
// environment env and its output in output. It returns a refusal when the
// sandbox cannot be built or verified (the binary did not run), a
// violation when the binary failed and reports a write the sandbox
// refused, and the binary's own error otherwise.
func runProtected(sb sandbox, pkg, dir string, env []string, output string, binary string, args ...string) error {
	spec, err := json.Marshal(sb)
	if err != nil {
		return err
	}
	file, err := os.Create(output)
	if err != nil {
		return err
	}
	cmd, err := sandboxCommand(binary, args...)
	if err != nil {
		file.Close()
		return refusal{err.Error()}
	}
	cmd.Dir = dir
	cmd.Env = append(env, sandboxVariable+"="+string(spec))
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

// scratch is the writable part of one run: a base directory made fresh
// for the run, outside the checkout, and in it the scratch directories,
// empty: tmp (TMPDIR and GOTMPDIR), cache (GOCACHE) and log (the test
// log). The names are short, because a test's unix socket path must fit in
// 108 bytes.
type scratch struct {
	base, tmp, cache, log string
}

func newScratch() (scratch, error) {
	base, err := os.MkdirTemp("", "ec")
	if err != nil {
		return scratch{}, err
	}
	if base, err = filepath.EvalSymlinks(base); err != nil {
		return scratch{}, err
	}
	s := scratch{base, filepath.Join(base, "t"), filepath.Join(base, "c"), filepath.Join(base, "l")}
	for _, dir := range s.dirs() {
		if err := os.Mkdir(dir, 0o700); err != nil {
			return scratch{}, err
		}
	}
	return s, nil
}

func (s scratch) dirs() []string { return []string{s.tmp, s.cache, s.log} }

// env is the environment that points the tests and the go command at the
// scratch directories.
func (s scratch) env() []string {
	return []string{"TMPDIR=" + s.tmp, "GOTMPDIR=" + s.tmp, "GOCACHE=" + s.cache}
}

// remove deletes the scratch directories. A test or the go command may
// leave a directory without write permission (the module cache does), so
// it makes each directory writable first.
func (s scratch) remove() error {
	filepath.WalkDir(s.base, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			os.Chmod(p, 0o700)
		}
		return nil
	})
	return os.RemoveAll(s.base)
}

// withEnv returns env with each entry of extra, replacing an entry of env
// with the same name.
func withEnv(env []string, extra ...string) []string {
	names := map[string]bool{}
	for _, kv := range extra {
		name, _, _ := strings.Cut(kv, "=")
		names[name] = true
	}
	var out []string
	for _, kv := range env {
		if name, _, _ := strings.Cut(kv, "="); !names[name] {
			out = append(out, kv)
		}
	}
	return append(out, extra...)
}

// checkSandboxAsATest builds a sandbox for the checkout root, with fresh
// scratch directories, and runs this test binary in it as a test would
// run, from dir: it checks the sandbox as the test sees it (checkSandbox)
// and every way to undo it (checkPrivileges). output keeps its report.
func checkSandboxAsATest(root, dir, output string) error {
	s, err := newScratch()
	if err != nil {
		return err
	}
	defer s.remove()
	self, err := os.Executable()
	if err != nil {
		return err
	}
	sb := sandbox{root, s.dirs()}
	op, err := json.Marshal(childOp{Op: "sandbox", Path: root, Scratch: sb.Scratch})
	if err != nil {
		return err
	}
	env := withEnv(sweepEnvironment(), append(s.env(), childVariable+"="+string(op))...)
	if err := runProtected(sb, "the sweep's own check", dir, env, output, self); err != nil {
		return err
	}
	var seen childResult
	b, err := os.ReadFile(output)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, &seen); err != nil {
		return fmt.Errorf("the check's report %q: %w", b, err)
	}
	if seen.Err != "" {
		return fmt.Errorf("the tests would not run in the sandbox: %s", seen.Err)
	}
	return nil
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
