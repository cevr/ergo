package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestHostFactsTellTmpfsFromDisk checks that the host facts of a temporary
// directory on tmpfs differ from those of one on disk, also for a directory
// the gate has not made yet.
func TestHostFactsTellTmpfsFromDisk(t *testing.T) {
	roots := newStorageRoots(t)
	disk, memory := readHostFacts(roots.disk), readHostFacts(filepath.Join(roots.memory, "step", "not-made"))
	if disk == memory {
		t.Fatalf("host facts are %q on disk and on tmpfs", disk)
	}
	if !strings.Contains(memory, "tmp 0x1021994;") {
		t.Errorf("host facts on tmpfs are %q, without the tmpfs magic", memory)
	}
}
