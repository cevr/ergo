package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// spoolRemedy is how to fix a refused output spool.
const spoolRemedy = "set ERGO_GATE_CACHE to a directory on disk, whose output directory (the spools) is on disk too"

// outputSpoolDir is the directory of the files that hold each step's output
// until the gate prints it: "output" in the gate cache, or in the temp
// directory when there is no cache. The gate then holds a few lines per step
// in its own memory, whatever a step prints, so a step's output cannot take
// the gate's reserve in its scope. A spool must be on disk: a file in memory
// (tmpfs) would charge the output to the gate's memory again, so the gate
// makes one spool here and refuses the directory when outputDestination
// refuses that file.
func outputSpoolDir(cacheDir string) (string, error) {
	base := cacheDir
	if base == "" {
		base = filepath.Join(os.TempDir(), "ergo-gate")
	}
	dir := filepath.Join(base, "output")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("output spool: %w", err)
	}
	spool, err := newOutputSpool(dir)
	if err != nil {
		return "", err
	}
	if err := spool.Close(); err != nil {
		return "", fmt.Errorf("output spool in %s: %w", dir, err)
	}
	return dir, nil
}

// newOutputSpool is an empty file in dir for one step's output. It is
// unlinked at once, so it leaves nothing behind however the gate ends, and
// it is refused when outputDestination refuses it.
func newOutputSpool(dir string) (*os.File, error) {
	file, err := os.CreateTemp(dir, "step-")
	if err != nil {
		return nil, fmt.Errorf("output spool in %s: %w", dir, err)
	}
	if err := os.Remove(file.Name()); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("output spool in %s: %w", dir, err)
	}
	if err := outputDestination(file); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("output spool in %s %w; %s", dir, err, spoolRemedy)
	}
	return file, nil
}
