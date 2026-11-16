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
	ergo := os.Getenv("ERGO_TEST_CLI")
	ctx, cancel := testwait.Context(t, context.Background())
	defer cancel()
	if ergo == "" {
		ergo = filepath.Join(t.TempDir(), "ergo")
		build := exec.CommandContext(ctx, "go", "build", "-buildvcs=false", "-o", ergo, "./cmd/ergo")
		build.Dir = repoRoot()
		build.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		build.Cancel = func() error { return syscall.Kill(-build.Process.Pid, syscall.SIGKILL) }
		if out, err := build.CombinedOutput(); err != nil {
			t.Fatalf("go build ./cmd/ergo: %v\n%s", err, out)
		}
	} else if !filepath.IsAbs(ergo) {
		ergo = filepath.Join(repoRoot(), ergo)
	}
	limit, err := proclimit.Limit(os.Environ(), proclimit.DefaultLimit)
	if err != nil {
		t.Fatal(err)
	}
	// The test's deadline stops a hung build or law run.
	cmd := exec.CommandContext(ctx, ergo, "test", "./check/store")
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.Dir = filepath.Join(repoRoot(), "compiler")
	var out strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &out
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := proclimit.Run(cmd, limit); err != nil {
		t.Fatalf("ergo test ./check/store in compiler/: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "PASS") {
		t.Fatalf("ergo test ./check/store printed no PASS:\n%s", out.String())
	}
}
