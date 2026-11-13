package main

import (
	"fmt"
	"os"
)

// diskFloorVariable sets the free bytes a step needs on the file systems it
// writes (its TMPDIR and the gate cache, which holds the merge tier's
// sandbox) before it starts, as a size such as 2G (parseBytes; 0 turns the
// check off). Below it the step is not started and fails with the reason
// named: a merge tier whose closure sweeps filled the disk failed them
// with ENOSPC partway (fu-gate-sandbox), a worse and slower failure than
// one that names the disk at the start.
const diskFloorVariable = "ERGO_GATE_DISK_FLOOR"

// defaultDiskFloor is the floor when diskFloorVariable is unset: about
// what one closure sweep's std store and per-run caches take at once
// (internal/closure's ergo-closure-std is 781 MB; measured, fu-gate-sandbox).
const defaultDiskFloor = 2 << 30

// freeBytes is the free bytes under path for an unprivileged writer; a test
// replaces it.
var freeBytes = statFree

// diskFloor reads diskFloorVariable.
func diskFloor() (uint64, error) {
	value, ok := os.LookupEnv(diskFloorVariable)
	if !ok || value == "" {
		return defaultDiskFloor, nil
	}
	if value == "0" {
		return 0, nil
	}
	floor, err := parseBytes(value)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", diskFloorVariable, err)
	}
	return uint64(floor), nil
}

// belowDiskFloor says why a step may not start: the first of paths whose
// file system has fewer than floor free bytes, or whose free bytes cannot
// be read. "" when every path is above the floor or the floor is 0.
func belowDiskFloor(floor uint64, paths ...string) string {
	if floor == 0 {
		return ""
	}
	for _, path := range paths {
		if path == "" {
			continue
		}
		free, err := freeBytes(path)
		if err != nil {
			return fmt.Sprintf("disk floor: cannot read the free space under %s: %v", path, err)
		}
		if free < floor {
			return fmt.Sprintf("disk floor: %d MiB free under %s, below the floor of %d MiB (%s); free space or lower the floor", free>>20, path, floor>>20, diskFloorVariable)
		}
	}
	return ""
}
