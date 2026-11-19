package closure

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/testinputs"
)

// reuseTargetVariable makes TestReuseTarget run.
const reuseTargetVariable = "ERGO_CLOSURE_REUSE_TARGET"

// TestReuseTarget reads in a fixture's checkout as a test package does:
// a file of its own testdata, a document outside its closure, and, through
// a child process, another one, which only the observer sees. It runs only
// as the target of TestRecordedRunReadsAsASweptRun.
func TestReuseTarget(t *testing.T) {
	if os.Getenv(reuseTargetVariable) == "" {
		t.Skip("the target of TestRecordedRunReadsAsASweptRun")
	}
	for _, name := range []string{"testdata/in/a.txt", "../../docs/tooling.md"} {
		if _, err := os.ReadFile(name); err != nil {
			t.Fatal(err)
		}
	}
	if out, err := exec.Command("cat", "../../docs/sub/x.md").CombinedOutput(); err != nil {
		t.Fatalf("cat: %v\n%s", err, out)
	}
	// A test that runs its own test binary again (internal/toolchain's
	// do): the child is observed with the rest and records nothing itself.
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(self, "-test.run=^$").CombinedOutput(); err != nil {
		t.Fatalf("the test binary again: %v\n%s", err, out)
	}
}

// TestRecordedRunReadsAsASweptRun: a test process run with
// testinputs.RecordVariable in the sweep's sandbox, as a go test -count=1
// step of the merge tier runs it once that tier gives its steps the
// sweep's guarantees, records the same inputs as the sweep's own run of
// the package, the child process's read included, and the sweep reuses
// it. The same run outside the sandbox boxes itself and is reused alike;
// with no bwrap to box it, it sees the checkout writable and is refused,
// and the refusal names the guarantee. Mutants: the recorder passes no
// test log (heavy-cost2-record-unobserved), no identity file
// (heavy-cost2-record-no-identity), probes nothing
// (heavy-cost2-record-tree-unprobed), does not box itself
// (heavy-cost2-record-unboxed), or leaves the record's variable to the
// tests, whose own test binary then records under the observer
// (heavy-cost2-record-inherited).
func TestRecordedRunReadsAsASweptRun(t *testing.T) {
	needSandbox(t)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	f := newFixture(t)
	const pkg = "internal/p"
	run := []string{"-test.run=^TestReuseTarget$"}
	target := reuseTargetVariable + "=1"
	swept, err := runTests(f.root, []string{f.root}, pkg, f.pkg, filepath.Join(t.TempDir(), "swept"), []string{target}, self, run...)
	if err != nil {
		t.Fatalf("the swept run: %v", err)
	}
	if !slices.Contains(swept, entry{"open", "docs/sub/x.md"}) {
		t.Fatalf("the swept run did not see the child's read: %v", swept)
	}

	// record runs the target with testinputs.RecordVariable, in the sweep's
	// sandbox or not, and with path as PATH when it is not "".
	record := func(sandboxed bool, path string) string {
		base, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		s, err := newScratch()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.remove() })
		env := withEnv(testEnv(f.pkg, target, testinputs.RecordVariable+"="+base), s.env()...)
		if path != "" {
			env = withEnv(env, "PATH="+path)
		}
		out := filepath.Join(t.TempDir(), "out")
		if sandboxed {
			deadline, err := runDeadline(0)
			if err != nil {
				t.Fatal(err)
			}
			sb := sandbox{Root: f.root, Scratch: append(s.dirs(), base)}
			if err := runProtectedWithin(sb, pkg, f.pkg, env, out, deadline, self, run...); err != nil {
				t.Fatalf("the recorded run: %v", err)
			}
		} else {
			cmd := exec.Command(self, run...)
			cmd.Dir, cmd.Env = f.pkg, env
			if b, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("the recorded run outside the sandbox: %v\n%s", err, b)
			}
		}
		return base
	}
	names := []string{"TestReuseTarget"}
	sweptEnv := withEnv(sweepEnvironment(), target)

	base := record(true, "")
	recs, dirs, err := testinputs.ReadRecords(base, pkg)
	if err != nil {
		t.Fatal(err)
	}
	if why := reuseRefusal(recs, dirs, pkg, f.pkg, names, sweptEnv); why != "" {
		t.Fatalf("the sweep refused the recorded run: %s", why)
	}
	reused, err := recordedEntries(recs, dirs, []string{f.root}, pkg)
	if err != nil {
		t.Fatal(err)
	}
	slices.SortFunc(swept, func(a, b entry) int { return strings.Compare(a.path+" "+a.op, b.path+" "+b.op) })
	slices.SortFunc(reused, func(a, b entry) int { return strings.Compare(a.path+" "+a.op, b.path+" "+b.op) })
	if !slices.Equal(reused, swept) {
		t.Errorf("the recorded run's inputs differ from the swept run's:\nrecorded %v\nswept    %v", reused, swept)
	}

	// Outside the sweep's sandbox, as a go test step runs it, the run
	// boxes itself in bwrap and records what the sweep's run records.
	base = record(false, "")
	recs, dirs, err = testinputs.ReadRecords(base, pkg)
	if err != nil {
		t.Fatal(err)
	}
	if why := reuseRefusal(recs, dirs, pkg, f.pkg, names, sweptEnv); why != "" {
		t.Fatalf("the sweep refused the run that boxed itself: %s", why)
	}
	boxed, err := recordedEntries(recs, dirs, []string{f.root}, pkg)
	if err != nil {
		t.Fatal(err)
	}
	slices.SortFunc(boxed, func(a, b entry) int { return strings.Compare(a.path+" "+a.op, b.path+" "+b.op) })
	if !slices.Equal(boxed, swept) {
		t.Errorf("the boxed run's inputs differ from the swept run's:\nrecorded %v\nswept    %v", boxed, swept)
	}

	// This package's tests make namespaces (testinputs.MakesNamespaces),
	// so a run of them that is not the target records nothing.
	quiet := t.TempDir()
	declared := exec.Command(self, "-test.run=^$")
	declared.Dir, declared.Env = f.pkg, withEnv(sweepEnvironment(), testinputs.RecordVariable+"="+quiet)
	if b, err := declared.CombinedOutput(); err != nil {
		t.Fatalf("a run of a package that makes namespaces: %v\n%s", err, b)
	}
	if recs, _, err := testinputs.ReadRecords(quiet, pkg); err != nil || len(recs) != 0 {
		t.Errorf("a package that makes namespaces recorded %d runs (%v); want none", len(recs), err)
	}

	// With no bwrap to box it, the run records unboxed, and the sweep
	// refuses it with the guarantee named.
	bin := t.TempDir()
	for _, name := range []string{"cat", "true"} {
		found, err := exec.LookPath(name)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(found, filepath.Join(bin, name)); err != nil {
			t.Fatal(err)
		}
	}
	base = record(false, bin)
	recs, dirs, err = testinputs.ReadRecords(base, pkg)
	if err != nil {
		t.Fatal(err)
	}
	if why := reuseRefusal(recs, dirs, pkg, f.pkg, names, withEnv(sweptEnv, "PATH="+bin)); !strings.Contains(why, "checkout writable") {
		t.Errorf("a run that saw the checkout writable: refusal %q; want it named", why)
	}
	// The sweep needs user namespaces on this host (needSandbox), so a run
	// left outside a box can make one, and the probe says so.
	if len(recs) != 1 || recs[0].UserNamespaces != "allowed" {
		t.Errorf("a run outside the sandbox: records %d, user namespaces %q; want 1, allowed", len(recs), recs[0].UserNamespaces)
	}
}
