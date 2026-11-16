package selfhostcheck

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"ergo.local/prototype/internal/proclimit"
	"ergo.local/prototype/internal/testwait"
)

// TestCompilerStoreLawsRun runs the laws of compiler/check/store, the
// self-hosted checker's tables (capstone S3a), through `ergo test`: the
// object view round trip, the pool's interning and its open bit, the
// substitution model and the cross-package identity of instances. It is
// the killer of compiler/check/store/testdata/mutants.json; mutants it
// alone catches: objref-kind-shift-short, pool-row-*,
// instance-list-drops-origin, open-bit-misses-param,
// subst-skips-row-resort, canon-*, view-context-reads-laws and
// declare-leaves-scope.
//
// It builds Go because `ergo test` lowers the laws into a test program and
// runs it: the check-time evaluator runs an operation type's laws, not a
// package's top-level laws, so no in-process check runs these.
func TestCompilerStoreLawsRun(t *testing.T) {
	needsBuild(t)
	runCompilerLaws(t, "./check/store")
}

// needsBuild marks a test that starts a go command or a built program: the
// fast tier (-short) skips it, and the full tier runs it (test-speed plan
// T3; docs/decisions.md, Q1 of 2026-11-08). Call it first in the test.
func needsBuild(t testing.TB) {
	t.Helper()
	if testing.Short() {
		t.Skip("full tier: builds Go")
	}
}

// runCompilerLaws runs `ergo test pkg` in compiler/ and fails unless every
// law passes.
func runCompilerLaws(t *testing.T, pkg string) {
	t.Helper()
	ctx, cancel := testwait.Context(t, context.Background())
	defer cancel()
	out, err := runCompiler(t, ctx, ergoCLI(t, ctx), "test", pkg)
	if err != nil {
		t.Fatalf("ergo test %s in compiler/: %v\n%s", pkg, err, out)
	}
	if !strings.Contains(out, "PASS") {
		t.Fatalf("ergo test %s printed no PASS:\n%s", pkg, out)
	}
}

// ergoCLI is the ergo command the tests run: ERGO_TEST_CLI, or one built
// from this checkout into the test's temporary directory.
func ergoCLI(t *testing.T, ctx context.Context) string {
	t.Helper()
	ergo := os.Getenv("ERGO_TEST_CLI")
	if ergo != "" {
		if !filepath.IsAbs(ergo) {
			ergo = filepath.Join(repoRoot(), ergo)
		}
		return ergo
	}
	ergo = filepath.Join(t.TempDir(), "ergo")
	build := exec.CommandContext(ctx, "go", "build", "-buildvcs=false", "-o", ergo, "./cmd/ergo")
	build.Dir = repoRoot()
	build.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	build.Cancel = func() error { return syscall.Kill(-build.Process.Pid, syscall.SIGKILL) }
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build ./cmd/ergo: %v\n%s", err, out)
	}
	return ergo
}

// runCompiler runs prog with args in compiler/, in its own process group,
// under the kernel memory limit; the test's deadline stops a hung run.
func runCompiler(t *testing.T, ctx context.Context, prog string, args ...string) (string, error) {
	t.Helper()
	limit, err := proclimit.Limit(os.Environ(), proclimit.DefaultLimit)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, prog, args...)
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.Dir = filepath.Join(repoRoot(), "compiler")
	var out strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &out
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	err = proclimit.Run(cmd, limit)
	return out.String(), err
}
