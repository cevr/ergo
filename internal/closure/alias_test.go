//go:build linux

package closure

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// The round-3 verifier's probes (round-3-receipts/probe-source.patch) as
// regression tests, and the guards of S2 and S3. Each probe reached a
// writable view of the checkout outside the round-3 sandbox, which made
// only the checkout's path read-only:
//
//   - TestVerifyWritableBindAlias: a bind mount of the checkout made
//     before the run, through which the test swapped a link it had read;
//   - TestVerifyWritableAliases/hardlink: a hard link of a checkout file
//     next to the checkout, through which the test changed the file;
//   - TestVerifyRootMovedAndRestored: the test moved the checkout's parent,
//     put a replacement at its name, read through it, and restored both.
//
// In the round-4 sandbox the whole file system is read-only (S1) and a
// checkout with another name is refused (S4), so each probe is refused or
// is a violation. Red at a11c58ba: each probe passes the sweep there.

// The variables of the probe targets.
const (
	aliasModeVariable  = "ERGO_CLOSURE_ALIAS_MODE"  // the target's mode
	aliasPathVariable  = "ERGO_CLOSURE_ALIAS_PATH"  // the alias it uses
	stageVariable      = "ERGO_CLOSURE_ALIAS_STAGE" // the stage runs
	parentNSVariable   = "ERGO_CLOSURE_PARENT_NS"   // the caller's mount namespace
	rootParentVariable = "ERGO_CLOSURE_ROOT_PARENT" // TestRootMoveTarget's directory to move
	rootOtherVariable  = "ERGO_CLOSURE_ROOT_OTHER"  // and its replacement
)

// TestAliasWriter is the probe target: it reads docs/tooling.md, which is
// outside internal/p's closure, and then changes the checkout through the
// alias, by its mode. It runs only as a target of the tests below.
func TestAliasWriter(t *testing.T) {
	mode, alias := os.Getenv(aliasModeVariable), os.Getenv(aliasPathVariable)
	if mode == "" {
		t.Skip("the target of TestWritableAliasesFailTheSweep")
	}
	switch mode {
	case "bind-alias":
		// TestVerifyMountAliasWriter: list docs, read docs/tooling.md
		// through testdata/away, then replace that link through the bind
		// alias with one to the package's own testdata.
		if _, err := os.ReadDir("../../docs"); err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile("testdata/away/tooling.md")
		if err != nil || !strings.Contains(string(b), "test package") {
			t.Fatalf("read docs: %v", err)
		}
		next := filepath.Join(alias, "internal/p/testdata/next")
		if err := os.Symlink("in", next); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(next, filepath.Join(alias, "internal/p/testdata/away")); err != nil {
			t.Fatal(err)
		}
	case "hardlink":
		// TestVerifyWritableAliases/hardlink: change docs/tooling.md
		// through its hard link next to the checkout.
		if err := os.WriteFile(alias, []byte("written"), 0o644); err != nil {
			t.Fatal(err)
		}
	case "mount":
		// A bind mount of the checkout into the test's TMPDIR.
		if err := bindMount("../..", os.TempDir()); err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatalf("%s=%q: no such mode", aliasModeVariable, mode)
	}
}

// TestRootMoveTarget is the round-3 verifier's TestVerifyRootMoveTarget:
// move the checkout's parent away, put the replacement at its name, read
// through the replacement by an absolute path, and restore both.
func TestRootMoveTarget(t *testing.T) {
	base := os.Getenv(rootParentVariable)
	if base == "" {
		t.Skip("the target of TestWritableAliasesFailTheSweep")
	}
	other := os.Getenv(rootOtherVariable)
	moved := base + "-moved"
	if err := os.Rename(base, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(other, base); err != nil {
		os.Rename(moved, base)
		t.Fatal(err)
	}
	defer func() {
		if err := os.Rename(base, other); err != nil {
			t.Error(err)
		}
		if err := os.Rename(moved, base); err != nil {
			t.Error(err)
		}
	}()
	b, err := os.ReadFile(base + "/m/internal/p/testdata/local/tooling.md")
	if err != nil || !strings.Contains(string(b), "test package") {
		t.Fatalf("original docs through the moved checkout: %v", err)
	}
}

// TestAliasStage runs in a user and mount namespace of its own, where it
// may mount: it binds a fixture's checkout next to it, as a host's bind
// mount would be, and runs the sweep on it. It runs only as the stage of
// TestWritableAliasesFailTheSweep/bind-alias.
func TestAliasStage(t *testing.T) {
	if os.Getenv(stageVariable) == "" {
		t.Skip("the stage of TestWritableAliasesFailTheSweep/bind-alias")
	}
	here, err := os.Readlink("/proc/self/ns/mnt")
	if err != nil {
		t.Fatal(err)
	}
	if here == os.Getenv(parentNSVariable) {
		t.Fatal("the stage is not in a mount namespace of its own; it mounts nothing")
	}
	if err := syscall.Mount("", "/", "", syscall.MS_REC|syscall.MS_PRIVATE, ""); err != nil {
		t.Fatal(err)
	}
	f := newFixture(t)
	alias := filepath.Join(f.outside, "alias")
	if err := os.Mkdir(alias, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := bindMount(f.root, alias); err != nil {
		t.Fatal(err)
	}
	defer syscall.Unmount(alias, 0)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	before := snapshot(t, f.root)
	stem := filepath.Join(t.TempDir(), "internal_p")
	entries, err := runTests(f.root, []string{f.root}, "internal/p", f.pkg, stem, []string{aliasModeVariable + "=bind-alias", aliasPathVariable + "=" + alias}, self, "-test.run=^TestAliasWriter$")
	var r refusal
	if !errors.As(err, &r) || !strings.Contains(r.Error(), "shows the checkout again") {
		t.Fatalf("the sweep did not refuse the checkout's bind alias at %s: err %v, recorded %v", alias, err, entries)
	}
	t.Log(r)
	if after := snapshot(t, f.root); fmt.Sprint(after) != fmt.Sprint(before) {
		t.Errorf("the checkout changed through its bind alias:\nbefore %v\nafter  %v", before, after)
	}
}

// TestWritableAliasesFailTheSweep: each round-3 probe, run as the sweep
// runs a package, is refused or is a closure violation, and the checkout
// and its parent are as they were.
func TestWritableAliasesFailTheSweep(t *testing.T) {
	needSandbox(t)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	t.Run("bind-alias", func(t *testing.T) {
		ns, err := os.Readlink("/proc/self/ns/mnt")
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(self, "-test.run=^TestAliasStage$", "-test.v")
		uid, gid := os.Getuid(), os.Getgid()
		cmd.SysProcAttr = &syscall.SysProcAttr{
			Cloneflags:  syscall.CLONE_NEWUSER | syscall.CLONE_NEWNS,
			UidMappings: []syscall.SysProcIDMap{{ContainerID: uid, HostID: uid, Size: 1}},
			GidMappings: []syscall.SysProcIDMap{{ContainerID: gid, HostID: gid, Size: 1}},
			AmbientCaps: []uintptr{capSysAdmin},
		}
		cmd.Env = append(sweepEnvironment(), stageVariable+"=1", parentNSVariable+"="+ns)
		out, err := cmd.CombinedOutput()
		if err != nil || !strings.Contains(string(out), "--- PASS: TestAliasStage") {
			t.Fatalf("the stage failed (%v):\n%s", err, out)
		}
		t.Logf("%s", out)
	})

	t.Run("hardlink", func(t *testing.T) {
		f := newFixture(t)
		file := filepath.Join(f.root, "docs/tooling.md")
		alias := filepath.Join(f.outside, "alias.md")
		if err := os.Link(file, alias); err != nil {
			t.Fatal(err)
		}
		before, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		stem := filepath.Join(t.TempDir(), "internal_p")
		entries, err := runTests(f.root, []string{f.root}, "internal/p", f.pkg, stem, []string{aliasModeVariable + "=hardlink", aliasPathVariable + "=" + alias}, self, "-test.run=^TestAliasWriter$")
		var r refusal
		if !errors.As(err, &r) || !strings.Contains(r.Error(), "hard link outside the checkout") {
			t.Fatalf("the sweep did not refuse the hard link %s: err %v, recorded %v", alias, err, entries)
		}
		t.Log(r)
		if after, err := os.ReadFile(file); err != nil || string(after) != string(before) {
			t.Fatalf("the checkout changed through the hard link: %q %v", after, err)
		}
		// Without the outside name, a write through the checkout's own
		// name is a violation.
		if err := os.Remove(alias); err != nil {
			t.Fatal(err)
		}
		entries, err = runTests(f.root, []string{f.root}, "internal/p", f.pkg, stem, []string{aliasModeVariable + "=hardlink", aliasPathVariable + "=" + file}, self, "-test.run=^TestAliasWriter$")
		wantViolation(t, err, entries)
	})

	t.Run("root-move", func(t *testing.T) {
		f := newFixture(t)
		baseID, err := identity(f.base)
		if err != nil {
			t.Fatal(err)
		}
		before := fmt.Sprint(snapshot(t, f.base), snapshot(t, f.other))
		stem := filepath.Join(t.TempDir(), "internal_p")
		entries, err := runTests(f.root, []string{f.root}, "internal/p", f.pkg, stem, []string{rootParentVariable + "=" + f.base, rootOtherVariable + "=" + f.other}, self, "-test.run=^TestRootMoveTarget$")
		wantViolation(t, err, entries)
		if id, err := identity(f.base); err != nil || id != baseID {
			t.Errorf("the checkout's parent moved: %v", err)
		}
		if after := fmt.Sprint(snapshot(t, f.base), snapshot(t, f.other)); after != before {
			t.Errorf("the run changed the checkout's parent or its replacement:\nbefore %v\nafter  %v", before, after)
		}
	})

	t.Run("parent-proc", func(t *testing.T) {
		// TestVerifyWritableAliases/parent-proc: the sweep's mount
		// namespace, which is writable, through /proc/<pid>/root. The
		// kernel denies it across user namespaces.
		f := newFixture(t)
		file := filepath.Join(f.root, "docs/tooling.md")
		r := runChild(t, f, childOp{Op: "write", Path: fmt.Sprintf("/proc/%d/root%s", os.Getpid(), file)})
		if r.Err == "" || r.Errno != "EACCES" {
			t.Fatalf("a write through the sweep's /proc/<pid>/root was not denied with EACCES: %+v", r)
		}
		if b, err := os.ReadFile(file); err != nil || string(b) != "the test package reads this document" {
			t.Fatalf("the checkout changed: %q %v", b, err)
		}
	})
}

// runChild runs one operation as a test of the fixture would, in a sandbox
// with fresh scratch directories, unobserved, and returns its result.
func runChild(t *testing.T, f fixture, op childOp) childResult {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	s, err := newScratch()
	if err != nil {
		t.Fatal(err)
	}
	defer s.remove()
	op.Path = strings.ReplaceAll(op.Path, tmpPrefix, s.tmp)
	op.Target = strings.ReplaceAll(op.Target, tmpPrefix, s.tmp)
	spec, err := json.Marshal(op)
	if err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(t.TempDir(), "child.out")
	env := testEnv(f.pkg, append(s.env(), childVariable+"="+string(spec))...)
	if err := runProtected(sandbox{f.root, s.dirs()}, "internal/p", f.pkg, env, output, self); err != nil {
		t.Fatalf("%+v: %v", op, err)
	}
	var r childResult
	b, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatalf("%+v: %q: %v", op, b, err)
	}
	return r
}

// TestScratchCannotAliasTheCheckout (S2): Linux refuses a hard link or a
// rename across mounts (EXDEV), so a test cannot give a checkout file a
// name in its scratch directory, which is a mount of its own; nor can it
// make a name in the read-only checkout for a scratch file (EROFS). A
// symbolic link in the scratch directory reaches the checkout read-only.
func TestScratchCannotAliasTheCheckout(t *testing.T) {
	needSandbox(t)
	f := newFixture(t)
	file := filepath.Join(f.root, "docs/tooling.md")
	for _, c := range []struct {
		op   childOp
		want string
	}{
		{childOp{Op: "link", Path: file, Target: tmpPrefix + "/h"}, "EXDEV"},
		{childOp{Op: "link", Path: filepath.Join(f.pkg, "testdata/in/a.txt"), Target: tmpPrefix + "/h"}, "EXDEV"},
		{childOp{Op: "rename", Path: file, Target: tmpPrefix + "/h"}, "EXDEV"},
		{childOp{Op: "link", Path: file, Target: filepath.Join(f.root, "h")}, "EROFS"},
		{childOp{Op: "link", Path: filepath.Join(f.outside, "plain.txt"), Target: filepath.Join(f.outside, "h")}, "EROFS"},
		{childOp{Op: "write", Path: filepath.Join(f.pkg, "testdata/away/tooling.md")}, "EROFS"},
		{childOp{Op: "write", Path: tmpPrefix + "/w"}, ""},
	} {
		r := runChild(t, f, c.op)
		if r.Errno != c.want {
			t.Errorf("%+v: got %q (%s), want %q", c.op, r.Errno, r.Err, c.want)
		}
	}
}

// TestSandboxHoldsNoPrivilege (S3): a test in the sandbox holds no
// capability in any set, has no_new_privs, and cannot remount, unmount or
// make a user namespace; and it sees S1 and S2 as checkSandbox states
// them. This is the check the sweep runs before any package.
func TestSandboxHoldsNoPrivilege(t *testing.T) {
	needSandbox(t)
	f := newFixture(t)
	if err := checkSandboxAsATest(f.root, f.pkg, filepath.Join(t.TempDir(), "sandbox.out")); err != nil {
		t.Fatal(err)
	}
	// The same check refuses a sandbox that is not one: the checkout's
	// view in this process is writable.
	s, err := newScratch()
	if err != nil {
		t.Fatal(err)
	}
	defer s.remove()
	if err := checkSandbox(sandbox{f.root, s.dirs()}); err == nil {
		t.Fatal("checkSandbox accepts the host's writable mounts")
	}
}

// TestMountAttemptsFailTheSweep: a test that tries a mount fails it (the
// tests hold no capability), and testinputs refuses the log of a run that
// tries one; the sweep does not accept the package.
func TestMountAttemptsFailTheSweep(t *testing.T) {
	needSandbox(t)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	f := newFixture(t)
	stem := filepath.Join(t.TempDir(), "internal_p")
	entries, err := runTests(f.root, []string{f.root}, "internal/p", f.pkg, stem, []string{aliasModeVariable + "=mount"}, self, "-test.run=^TestAliasWriter$")
	if err == nil {
		t.Fatalf("the sweep accepted a run that tried a mount; recorded %v", entries)
	}
	t.Log(err)
}
