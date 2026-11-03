package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestToolDigestKeysHostFacts checks, on real file systems, that a step's
// tool digest, which keys Go's test cache for the tests the step runs,
// changes when the file system behind the same TMPDIR or the same GOTMPDIR
// (where t.TempDir makes the tests' directories) changes from disk to tmpfs,
// and comes back when it changes back. Each variable names a link that the
// test points at a directory on disk or on tmpfs, so only the file system
// behind the path changes, as when a host mounts tmpfs there.
func TestToolDigestKeysHostFacts(t *testing.T) {
	roots := newStorageRoots(t)
	places, root := t.TempDir(), t.TempDir()
	tmp, goTmp := filepath.Join(places, "tmp"), filepath.Join(places, "gotmp")
	point := func(link, target string) {
		t.Helper()
		if err := os.Remove(link); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
	}
	g := &gate{root: root}
	step := &Step{Name: "go test probe"}
	digest := func(tmpTarget, goTmpTarget string) string {
		t.Helper()
		point(tmp, tmpTarget)
		point(goTmp, goTmpTarget)
		return g.toolDigest(step)
	}
	t.Setenv("TMPDIR", tmp)
	t.Setenv("GOTMPDIR", goTmp)
	disk := digest(roots.disk, roots.disk)
	if digest(roots.memory, roots.disk) == disk {
		t.Error("moving the step's temporary directory from disk to tmpfs left the tool digest unchanged")
	}
	if digest(roots.disk, roots.memory) == disk {
		t.Error("moving the step's GOTMPDIR from disk to tmpfs left the tool digest unchanged")
	}
	if digest(roots.disk, roots.disk) != disk {
		t.Error("the same file systems gave a different tool digest")
	}
}

// TestHostFactsTellTmpfsFromDisk checks that the host facts of a temporary
// directory on tmpfs differ from those of one on disk, also for a directory
// the gate has not made yet; and so do those of a GOTMPDIR on tmpfs and on
// disk, and of a GOTMPDIR set and unset.
func TestHostFactsTellTmpfsFromDisk(t *testing.T) {
	roots := newStorageRoots(t)
	disk, memory := readHostFacts(roots.disk, roots.disk), readHostFacts(filepath.Join(roots.memory, "step", "not-made"), roots.disk)
	if disk == memory {
		t.Fatalf("host facts are %q on disk and on tmpfs", disk)
	}
	if !strings.Contains(memory, "tmp 0x1021994;") {
		t.Errorf("host facts on tmpfs are %q, without the tmpfs magic", memory)
	}
	goTmpMemory := readHostFacts(roots.disk, filepath.Join(roots.memory, "gotmp", "not-made"))
	if goTmpMemory == disk {
		t.Fatalf("host facts are %q with GOTMPDIR on disk and on tmpfs", disk)
	}
	if !strings.Contains(goTmpMemory, "gotmp 0x1021994;") {
		t.Errorf("host facts with GOTMPDIR on tmpfs are %q, without the tmpfs magic", goTmpMemory)
	}
	if unset := readHostFacts(roots.disk, ""); unset == disk || !strings.Contains(unset, "gotmp unset;") {
		t.Errorf("host facts with GOTMPDIR unset are %q, with it on disk %q", unset, disk)
	}
}
