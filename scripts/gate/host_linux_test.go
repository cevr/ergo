package main

import (
	"path/filepath"
	"strings"
	"testing"
)

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
