package toolchain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"

	"ergo.local/prototype/toolchain/ir"
)

// TestApplyPatchFindsMovedHunksAndRefusesChangedOnes: a hunk applies
// where its old lines stand, also when the release moved them (patch
// releases move the compiler's lines: unified.go differs between go1.27.0
// and go1.27.3), and never where they changed. Mutants: the hunk applies
// only at its stated line (irt-apply-no-offset); a changed line is taken
// as a match (irt-apply-fuzz).
func TestApplyPatchFindsMovedHunksAndRefusesChangedOnes(t *testing.T) {
	patch := "diff --git a/f.go b/f.go\n--- a/f.go\n+++ b/f.go\n@@ -2,3 +2,3 @@\n b\n-c\n+C\n d\n" +
		"diff --git a/new.go b/new.go\nnew file mode 100644\n--- /dev/null\n+++ b/new.go\n@@ -0,0 +1,2 @@\n+x\n+y\n"
	for _, row := range []struct {
		name, file, want string
		rejected         int
	}{
		{"in place", "a\nb\nc\nd\ne\n", "a\nb\nC\nd\ne\n", 0},
		{"moved down", "0\n1\na\nb\nc\nd\ne\n", "0\n1\na\nb\nC\nd\ne\n", 0},
		{"context changed", "a\nB\nc\nd\ne\n", "a\nB\nc\nd\ne\n", 1},
	} {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "f.go"), []byte(row.file), 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := ApplyPatch(root, []byte(patch))
		if err != nil {
			t.Fatalf("%s: %v", row.name, err)
		}
		if len(got.Rejected) != row.rejected || got.Hunks != 2 {
			t.Errorf("%s: %d of %d hunks rejected (%v), want %d of 2", row.name, len(got.Rejected), got.Hunks, got.Rejected, row.rejected)
		}
		if row.rejected == 0 && string(got.Files["f.go"]) != row.want {
			t.Errorf("%s: f.go is %q, want %q", row.name, got.Files["f.go"], row.want)
		}
		if string(got.Files["new.go"]) != "x\ny\n" {
			t.Errorf("%s: new.go is %q", row.name, got.Files["new.go"])
		}
	}
}

// fakeRelease writes a Go release of version with the one source file the
// planted patches edit, and returns its GOROOT.
func fakeRelease(t *testing.T, version string) string {
	t.Helper()
	root := t.TempDir()
	for name, data := range map[string]string{
		"VERSION":                              version + "\ntime 2026-11-12T00:00:00Z\n",
		"bin/go":                               "go of " + root,
		"pkg/include/asm.h":                    "",
		"pkg/tool/" + hostTools() + "/compile": "stock compile",
		"pkg/tool/" + hostTools() + "/link":    "stock link",
		"src/cmd/compile/x.go":                 "a\nb\nc\n",
	} {
		file := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(data), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// plantedLock is a lock that ports version to a patch that turns x.go's b
// into to.
func plantedLock(version, to string) fstest.MapFS {
	patch := "diff --git a/src/cmd/compile/x.go b/src/cmd/compile/x.go\n--- a/src/cmd/compile/x.go\n+++ b/src/cmd/compile/x.go\n@@ -1,3 +1,3 @@\n a\n-b\n+" + to + "\n c\n"
	sum := sha256.Sum256([]byte(patch))
	lock := fmt.Sprintf("patch p.patch sha256:%s\nport %s p.patch\n", hex.EncodeToString(sum[:]), version)
	return fstest.MapFS{"lock": {Data: []byte(lock)}, "p.patch": {Data: []byte(patch)}}
}

// fakeBuild stands for go build of cmd/compile: it checks that the
// overlay replaces x.go with its patched text and writes the compile.
func fakeBuild(builds *atomic.Int32) func(source, overlay, out string) error {
	return func(source, overlay, out string) error {
		builds.Add(1)
		data, err := os.ReadFile(overlay)
		if err != nil {
			return err
		}
		var o struct{ Replace map[string]string }
		if err := json.Unmarshal(data, &o); err != nil {
			return err
		}
		patched, err := os.ReadFile(o.Replace[filepath.Join(source, "src", "cmd", "compile", "x.go")])
		if err != nil {
			return err
		}
		return os.WriteFile(out, append([]byte("compile of "), patched...), 0o755)
	}
}

// TestPatchBuildsOnceAndKeysOnThePatch: the patched compile is built once
// per release and patch, from the release's own source with the patch
// applied, into a GOROOT that is the release but for compile; another
// patch of the same release is another compile, and a patch whose content
// is not the lock's is refused. Mutants: the key leaves the patch out
// (irt-cache-key-without-patch); the patch's digest is not compared with
// the lock (irt-patch-digest-unchecked); a ready artifact is built again
// (irt-patched-never-ready).
func TestPatchBuildsOnceAndKeysOnThePatch(t *testing.T) {
	var builds atomic.Int32
	cache, source := t.TempDir(), fakeRelease(t, "go1.27.0")
	c := PatchConfig{Cache: cache, Source: source, lock: plantedLock("go1.27.0", "B"), build: fakeBuild(&builds)}
	first, err := Patch(c)
	if err != nil {
		t.Fatal(err)
	}
	again, err := Patch(c)
	if err != nil || again != first || builds.Load() != 1 {
		t.Fatalf("second Patch: %+v %v after %d builds; want %+v after 1", again, err, builds.Load(), first)
	}
	if first.Release != "go1.27.0" || first.Go != filepath.Join(first.Root, "bin", "go"+exe()) {
		t.Errorf("Patch: %+v", first)
	}
	tools := filepath.Join("pkg", "tool", hostTools())
	if data, _ := os.ReadFile(filepath.Join(first.Root, tools, "compile")); string(data) != "compile of a\nB\nc\n" {
		t.Errorf("the patched compile is %q", data)
	}
	for _, name := range []string{filepath.Join("bin", "go"), filepath.Join(tools, "link"), filepath.Join("pkg", "include", "asm.h"), "VERSION"} {
		a, errA := os.Stat(filepath.Join(first.Root, name))
		b, errB := os.Stat(filepath.Join(source, name))
		if errA != nil || errB != nil || !os.SameFile(a, b) {
			t.Errorf("%s of the patched GOROOT is not the release's: %v %v", name, errA, errB)
		}
	}
	other := c
	other.lock = plantedLock("go1.27.0", "X")
	second, err := Patch(other)
	if err != nil || second.Root == first.Root || builds.Load() != 2 {
		t.Fatalf("another patch: %+v %v after %d builds; want a new compile", second, err, builds.Load())
	}
	if data, _ := os.ReadFile(filepath.Join(second.Root, tools, "compile")); string(data) != "compile of a\nX\nc\n" {
		t.Errorf("the second patched compile is %q", data)
	}
	tampered := plantedLock("go1.27.0", "B")
	tampered["p.patch"] = &fstest.MapFile{Data: []byte(strings.Replace(string(tampered["p.patch"].Data), "+B", "+Z", 1))}
	bad := c
	bad.lock = tampered
	if got, err := Patch(bad); err == nil || !strings.Contains(err.Error(), "does not lock") {
		t.Errorf("a patch the lock does not lock: %+v %v", got, err)
	}
}

// TestPatchBuildsOnceForTwoBuilds: a second build that starts while the
// first builds the compile waits on the release's lock and then uses the
// first one's. Mutants: no lock (irt-patch-no-lock); no second look under
// the lock (irt-patch-no-recheck).
func TestPatchBuildsOnceForTwoBuilds(t *testing.T) {
	if !locks {
		t.Skip("no install lock on this system")
	}
	var builds atomic.Int32
	entered, release := make(chan struct{}, 2), make(chan struct{})
	inner := fakeBuild(&builds)
	c := PatchConfig{Cache: t.TempDir(), Source: fakeRelease(t, "go1.27.0"), lock: plantedLock("go1.27.0", "B"),
		build: func(source, overlay, out string) error {
			entered <- struct{}{}
			<-release
			return inner(source, overlay, out)
		}}
	var wg sync.WaitGroup
	got := make([]Patched, 2)
	errs := make([]error, 2)
	wg.Add(1)
	go func() { defer wg.Done(); got[0], errs[0] = Patch(c) }()
	<-entered
	waiting := make(chan struct{})
	second := c
	second.waiting = func() { close(waiting) }
	wg.Add(1)
	go func() { defer wg.Done(); got[1], errs[1] = Patch(second) }()
	select {
	case <-waiting:
	case <-entered:
		close(release)
		wg.Wait()
		t.Fatal("the second build ran while the first held the release")
	}
	close(release)
	wg.Wait()
	if errs[0] != nil || errs[1] != nil || got[0] != got[1] || builds.Load() != 1 {
		t.Fatalf("builds: %+v %v, %+v %v; %d compiles, want 1", got[0], errs[0], got[1], errs[1], builds.Load())
	}
}

// TestPatchRefusesAnUnportedGo: ergo patches only a release the lock ports
// the patch to, and the release it pins is one. Mutants: the port is
// looked up by the pin instead of the release the build runs
// (irt-wrong-go-pinned); the pin is a release with no port
// (irt-pin-unported).
func TestPatchRefusesAnUnportedGo(t *testing.T) {
	var builds atomic.Int32
	_, err := Patch(PatchConfig{Cache: t.TempDir(), Source: fakeRelease(t, "go1.27.1"), build: fakeBuild(&builds)})
	if err == nil || !strings.Contains(err.Error(), "Go go1.27.1 has no port") || !strings.Contains(err.Error(), Version) {
		t.Errorf("an unported go1.27.1: %v; want the refusal that lists the ports", err)
	}
	if builds.Load() != 0 {
		t.Errorf("an unported release built %d compiles", builds.Load())
	}
	if _, err := portOf(ir.Files, Version); err != nil {
		t.Errorf("the pinned %s: %v", Version, err)
	}
}
