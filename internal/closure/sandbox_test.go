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
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The sweep runs every test binary in a sandbox built as Bazel's
// linux-sandbox builds one: the whole file system is read-only, and only
// scratch directories made fresh and empty for the run are writable. The
// tests and every process they start then cannot change the checkout or
// anything above it, by any name. A process outside the tree can (a host
// service the tests reach by a socket, which acts for them): the sandbox
// does not stop it, so the sweep does not infer from the sandbox that a path
// names after the run what it named at access time. It classifies each path
// by the access-time identity the observer recorded and fails closed where
// the two differ (readLog, testinputs identity mode). That covers only part
// of what a process outside the tree can do; the rest is a known limit
// (docs/tooling.md, the unit sandbox-host-services). The sandbox is a rule
// of construction, not an inference from the log:
//
//   - S1: every mount of the sandbox's mount namespace is read-only,
//     recursively from the root of the file system: the checkout, each of
//     its ancestors, and any bind mount of it made before the run.
//   - S2: the only writable places are the run's scratch directories
//     (TMPDIR and GOTMPDIR; XDG_CACHE_HOME, which holds GOCACHE; the test
//     log's directory; a fresh tmpfs at /dev/shm), each a mount of its
//     own, outside the checkout and not an ancestor of it. A package may
//     declare a cgroup2 mount it writes (declaredKernelWrites); cgroup2
//     holds no file that could be a name of a checkout file.
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
	// reaperArgument, the first argument of a helper run, makes it the
	// run's reaper (reaperMain), which starts the helper with the rest.
	reaperArgument = "-closure.reaper"
)

// sandbox is what one protected run needs: the checkout, by its physical
// path, the scratch directories that stay writable, and the mount points
// of the cgroup2 file systems the package declares it writes
// (declaredKernelWrites).
type sandbox struct {
	Root    string
	Scratch []string
	Kernel  []string `json:",omitempty"`
	// Shared are the scratch directories that need not be empty: the Go
	// build cache that the runs of one package share (runPlan). It was
	// made empty for the package, and only the package's runs wrote it,
	// each in its sandbox, where no run can make a hard link of a checkout
	// file in it (EXDEV, S2) or write the checkout through one.
	Shared []string `json:",omitempty"`
}

// helperMain runs the helper modes of this test binary, from TestMain,
// before testinputs.Main. It returns only when the binary runs as a test.
func helperMain() {
	if spec, ok := os.LookupEnv(sandboxVariable); ok {
		if len(os.Args) > 1 && os.Args[1] == reaperArgument {
			reaperMain(os.Args[2:])
		}
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

// runTimeout is a run that did not end within its deadline (runDeadline):
// the sweep stopped it. left are the run's processes at the deadline.
type runTimeout struct {
	pkg, output string
	deadline    time.Duration
	left        []string
}

func (r runTimeout) Error() string {
	return fmt.Sprintf("%s: the run did not end within %v (%s, or this test's own deadline), so the sweep stopped it; its processes then:\n  %s\noutput %s", r.pkg, r.deadline.Round(time.Second), runDeadlineVariable, strings.Join(r.left, "\n  "), r.output)
}

// runDeadlineVariable bounds each run of a test binary in the sandbox (a
// Go duration; 20 minutes by default). A run that has not ended by then is
// a failure that names its package and the processes the run still had,
// and its process group is stopped, so a run that hangs (an observed child
// stuck in its start, for example) leaves no process behind.
const runDeadlineVariable = "ERGO_CLOSURE_RUN_DEADLINE"

// runReportMargin is the time a run's deadline leaves before this test
// binary's own deadline (binaryDeadline): reaperBound (30 s, on Linux) for
// the run's reaper to collect the run, and 30 s for the test to report
// every package's verdict before the testing package's timeout panics.
const runReportMargin = time.Minute

// processStart is when this test binary started, before the testing
// package started its -test.timeout alarm.
var processStart = time.Now()

// binaryDeadline is when this test binary's -test.timeout ends, or false
// when it has none. It counts from processStart, so it is no later than
// the testing package's alarm.
func binaryDeadline() (time.Time, bool) {
	f := flag.Lookup("test.timeout")
	if f == nil {
		return time.Time{}, false
	}
	getter, ok := f.Value.(flag.Getter)
	if !ok {
		return time.Time{}, false
	}
	timeout, ok := getter.Get().(time.Duration)
	if !ok || timeout <= 0 {
		return time.Time{}, false
	}
	return processStart.Add(timeout), true
}

// runDeadline is the bound of one run that starts now: runDeadlineVariable
// (20 minutes by default), and never past runReportMargin before this test
// binary's own deadline. At batch 9 a swept run that did not end held the
// sweep until the step's test timeout panicked, which named no package and
// reported no verdict (b9-fix-gate). A run started with less time left than
// the margin gets a second, so it fails at once with its package named. A
// run holds at most sweepRunBudget of recorded test time (planRuns), so it
// ends well within the 20 minutes; the gate gives the sweep step a test
// timeout long enough for every run of a package in turn (scripts/gate,
// sweepTimeout), so the binary's deadline bounds only a sweep that hangs.
func runDeadline() (time.Duration, error) {
	d := 20 * time.Minute
	if value := os.Getenv(runDeadlineVariable); value != "" {
		parsed, err := time.ParseDuration(value)
		if err != nil || parsed <= 0 {
			return 0, fmt.Errorf("%s=%q: want a positive duration", runDeadlineVariable, value)
		}
		d = parsed
	}
	if end, ok := binaryDeadline(); ok {
		d = max(min(d, time.Until(end)-runReportMargin), time.Second)
	}
	return d, nil
}

// runProtected runs binary with args from dir in the sandbox sb, with the
// environment env and its output in output. It returns a refusal when the
// sandbox cannot be built or verified (the binary did not run), a
// violation when the binary failed and reports a write the sandbox
// refused, and the binary's own error otherwise. The run's bound is
// runDeadline.
func runProtected(sb sandbox, pkg, dir string, env []string, output string, binary string, args ...string) error {
	deadline, err := runDeadline()
	if err != nil {
		return err
	}
	return runProtectedWithin(sb, pkg, dir, env, output, deadline, binary, args...)
}

// runTestTimeout is the -test.timeout of a test binary whose run is
// stopped after run: the run less a quarter of it, at most 30 s, the time
// the binary needs to start and to report its timeout. A test that derives
// a bound from t.Deadline (a native go test it starts, for one) then fails
// within the run, naming the stuck test, before the sweep stops the run,
// which names only the package. With the fixed 45 minutes the sweep gave
// every run, a hang in a native harness of internal/sketch/lower held the
// run until its 20 minute stop and named no test (fu-lower-flake M1).
func runTestTimeout(run time.Duration) time.Duration {
	return run - min(run/4, 30*time.Second)
}

// runProtectedWithin is runProtected with the run's bound, deadline.
func runProtectedWithin(sb sandbox, pkg, dir string, env []string, output string, deadline time.Duration, binary string, args ...string) error {
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
	timedOut, left, runErr := runWithin(cmd, deadline)
	file.Close()
	if timedOut {
		return runTimeout{pkg: pkg, output: output, deadline: deadline, left: left}
	}
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
	return fmt.Errorf("the tests failed (%v), so the inputs they record are incomplete; its failure lines:\n%s\noutput %s ends:\n%s", runErr, failureLines(out), output, tail)
}

// failureLine is a line of go test output that names a failure: a test's
// --- FAIL line, a panic (a -test.timeout panic, with the running tests
// after it), or a t.Error or t.Fatal message, file:line first, which
// carries the seed of a property's failing case.
var failureLine = regexp.MustCompile(`^\s*(--- FAIL: |panic: |running tests:|\S+_test\.go:\d+: )`)

// failureLines are the output's failure lines and the line after each, in
// order, at most 40 of them, each cut to 300 bytes; after "running tests:"
// they are every running test it lists, one a line indented a tab past
// it, so a timeout names the stuck subtest, not only its top test. The tail of a
// long failure shows only its last lines (at fu-lower-flake, the lowered
// Go that a native test's message prints last); these name the failing
// test and its message first, wherever they are in the output.
func failureLines(out []byte) string {
	const most, width = 40, 300
	lines := strings.Split(string(out), "\n")
	var kept []string
	// listed is the prefix of a running test's line after "running tests:":
	// that line's indent (a native test's output is nested) and a tab.
	next, listed := -1, ""
	for i, line := range lines {
		hit := failureLine.MatchString(line)
		running := listed != "" && strings.HasPrefix(line, listed)
		if !running {
			listed = ""
		}
		if !hit && i != next && !running {
			continue
		}
		if hit {
			next = i + 1
			if strings.TrimSpace(line) == "running tests:" {
				listed = line[:len(line)-len(strings.TrimLeft(line, " \t"))] + "\t"
			}
		}
		if len(kept) == most {
			kept = append(kept, "  ... (more failure lines in the output)")
			break
		}
		if len(line) > width {
			line = line[:width] + " ..."
		}
		kept = append(kept, "  "+line)
	}
	if len(kept) == 0 {
		return "  (none: the output names no failing test)"
	}
	return strings.Join(kept, "\n")
}

// scratch is the writable part of one run: a base directory made fresh
// for the run, outside the checkout, and in it the scratch directories,
// empty: tmp (TMPDIR and GOTMPDIR), cache (XDG_CACHE_HOME, so GOCACHE and
// the user cache that ergo run stages programs in) and log (the test log).
// The names are short, because a test's unix socket path must fit in 108
// bytes.
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
	return []string{"TMPDIR=" + s.tmp, "GOTMPDIR=" + s.tmp, "XDG_CACHE_HOME=" + s.cache, "GOCACHE=" + filepath.Join(s.cache, "go-build")}
}

// remove deletes the scratch directories. A test or the go command may
// leave a directory without write permission (the module cache does), so
// it makes each directory writable first.
func (s scratch) remove() error { return removeTree(s.base) }

// removeTree deletes dir, making each directory in it writable first.
func removeTree(dir string) error {
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			os.Chmod(p, 0o700)
		}
		return nil
	})
	return os.RemoveAll(dir)
}

// newSharedCache makes the Go build cache that the runs of one package
// share (runPlan): an empty directory outside the checkout, by its
// physical path, writable in each run's sandbox as a scratch directory is.
// The go command locks and writes its entries so that processes can share
// one cache at once.
func newSharedCache() (string, error) {
	dir, err := os.MkdirTemp("", "eg")
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(dir)
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
	sb := sandbox{Root: root, Scratch: s.dirs()}
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
	// A fixture's run takes seconds: one that hangs fails within a minute,
	// well inside the test's own deadline.
	t.Setenv(runDeadlineVariable, "60s")
}
