//go:build linux

package build

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/proclimit"
)

// probeEnvVariable is the environment the root-parent helper gives its
// probe, as KEY=VALUE entries one per line.
const probeEnvVariable = "ERGO_BUILD_TEST_PROBE_ENV"

// rootHelpers runs the helper modes of this file.
func rootHelpers(mode string) (bool, int) {
	switch mode {
	case "root":
		return true, rootProbeHelper()
	case "root-parent":
		return true, rootParentHelper()
	}
	return false, 0
}

// rootProbeHelper is helper mode "root": it prints DefaultRoot's result,
// "root DIR" or "refused REASON".
func rootProbeHelper() int {
	root, err := DefaultRoot()
	if err != nil {
		fmt.Println("refused", err)
		return 0
	}
	fmt.Println("root", root)
	return 0
}

// rootParentHelper is helper mode "root-parent", a Go test binary (its
// arguments are testing flags) started with the environment the test
// chose: it runs this binary as a probe through a link whose name is no
// test's, with no arguments and only the environment probeEnvVariable
// holds, as a test that forgets the private cache runs ergo, and prints
// what the probe printed.
func rootParentHelper() int {
	self, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	link := filepath.Join(os.Getenv(rootVariable), "ergo-root-probe")
	if err := os.Symlink(self, link); err != nil && !os.IsExist(err) {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	probe := exec.Command(link)
	probe.Env = append(strings.Split(os.Getenv(probeEnvVariable), "\n"), helperVariable+"=root")
	probe.Stderr = os.Stderr
	out, err := probe.Output()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	os.Stdout.Write(out)
	return 0
}

// TestForgottenCacheIsRefused: a program that a Go test starts with an
// environment built by hand, with neither ERGO_CACHE nor XDG_CACHE_HOME,
// is refused the user's cache: the one the test binary's own starting
// environment resolves to, ERGO_CACHE aside (the test's parent sets one,
// as a caller with an exported ERGO_CACHE would). The probe is no test
// binary, so the rule finds the test through its ancestry. With
// XDG_CACHE_HOME naming another directory the probe builds there. The
// rule runs over a proc tree the test writes and in this process too, so
// coverage, which a child's run does not reach, names the test for the
// mutant runner; then over real processes, each under a memory bound the
// kernel enforces. Mutants: the
// refusal off (cache-bound-forgotten-not-refused), the ancestry not
// walked (cache-bound-ancestry-unread), the starting environment not read
// (cache-bound-environ-unread), ERGO_CACHE counted in the test's starting
// environment (cache-bound-ancestor-own-counted).
func TestForgottenCacheIsRefused(t *testing.T) {
	home := t.TempDir()

	// In this process, over a proc tree written here, so the mutant
	// runner's coverage sees the rule run: a program (pid 300) whose parent
	// (200) is a test started with HOME and an ERGO_CACHE of its own.
	proc := t.TempDir()
	process := func(pid int, ppid int, cmdline, environ []string) {
		dir := filepath.Join(proc, fmt.Sprint(pid))
		mustWrite(t, filepath.Join(dir, "cmdline"), strings.Join(cmdline, "\x00")+"\x00")
		mustWrite(t, filepath.Join(dir, "environ"), strings.Join(environ, "\x00")+"\x00")
		mustWrite(t, filepath.Join(dir, "stat"), fmt.Sprintf("%d (a) b) S %d %d 0 0", pid, ppid, pid))
	}
	process(300, 200, []string{"/usr/bin/ergo", "build", "main.ergo"}, []string{"HOME=" + home})
	process(200, 1, []string{"/tmp/go-build1/b001/build.test", "-test.run=^TestX$"}, []string{"PATH=/usr/bin", "HOME=" + home, RootVariable + "=" + filepath.Join(home, "private")})
	if test, refused := testRootsIn(proc, 300, filepath.Join(home, ".cache", "ergo")); !refused || test != "pid 200" {
		t.Errorf("over the written proc tree, the user's cache of test 200 got (%q, %v); want it refused under pid 200", test, refused)
	}
	if _, refused := testRootsIn(proc, 300, filepath.Join(home, "other", "ergo")); refused {
		t.Error("over the written proc tree, a cache of the program's own was refused")
	}
	// This test process itself, with the cache its starting environment
	// names: refused.
	environ, err := os.ReadFile("/proc/self/environ")
	if err != nil {
		t.Fatal(err)
	}
	started := lookupIn(environ)
	t.Setenv(RootVariable, "")
	t.Setenv("XDG_CACHE_HOME", started("XDG_CACHE_HOME"))
	t.Setenv("HOME", started("HOME"))
	if root, err := DefaultRoot(); err == nil || !strings.Contains(err.Error(), "under the Go test pid ") {
		t.Errorf("a test process with its starting cache variables resolved %q (%v); want the user's cache refused", root, err)
	}

	// Real processes: a test binary, and a program it starts with an
	// environment built by hand.
	run := func(probeEnv ...string) string {
		t.Helper()
		parent := helper(home, "root-parent")
		// The parent keeps the rest of this process's environment (a
		// coverage run's GOCOVERDIR, for one); the variables that name a
		// cache are the test's choice.
		parent.Env = slices.DeleteFunc(os.Environ(), func(entry string) bool {
			name, _, _ := strings.Cut(entry, "=")
			return slices.Contains([]string{"HOME", "XDG_CACHE_HOME", RootVariable, helperVariable, rootVariable, probeEnvVariable}, name)
		})
		parent.Env = append(parent.Env, "HOME="+home, RootVariable+"="+filepath.Join(home, "private"),
			helperVariable+"=root-parent", rootVariable+"="+home, probeEnvVariable+"="+strings.Join(probeEnv, "\n"))
		var out, errs strings.Builder
		parent.Stdout, parent.Stderr = &out, &errs
		if err := proclimit.Start(parent, 512<<20); err != nil {
			t.Fatal(err)
		}
		if err := parent.Wait(); err != nil {
			t.Fatalf("root-parent: %v\n%s%s", err, out.String(), errs.String())
		}
		return strings.TrimSpace(out.String())
	}
	if got := run("HOME=" + home); !strings.HasPrefix(got, "refused ") || !strings.Contains(got, "under the Go test pid ") {
		t.Errorf("a program a test started with HOME alone got %q; want the user's cache refused", got)
	}
	other := filepath.Join(home, "other")
	if got, want := run("HOME="+home, "XDG_CACHE_HOME="+other), "root "+filepath.Join(other, "ergo"); got != want {
		t.Errorf("with its own XDG_CACHE_HOME the program got %q, want %q", got, want)
	}
}

// TestTestBinaryIsNamedOrFlagged: a process is a Go test binary when its
// program's name ends in .test (go test's name, run with no flags) or an
// argument is a testing flag (a binary of go test -c -o NAME), and not
// otherwise. Mutants: either sign unread (cache-bound-test-name-unread,
// cache-bound-test-flag-unread).
func TestTestBinaryIsNamedOrFlagged(t *testing.T) {
	for _, c := range []struct {
		args []string
		test bool
	}{
		{[]string{"/tmp/go-build1/b001/build.test"}, true},
		{[]string{"/tmp/probe", "-test.run=^TestX$"}, true},
		{[]string{"/usr/bin/ergo", "test", "main.ergo", "--run", "X"}, false},
	} {
		dir := t.TempDir()
		mustWrite(t, filepath.Join(dir, "cmdline"), strings.Join(c.args, "\x00")+"\x00")
		if got := testBinary(dir); got != c.test {
			t.Errorf("testBinary(%q) = %v, want %v", c.args, got, c.test)
		}
	}
}
