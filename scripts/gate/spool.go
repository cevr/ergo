package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// outputSpoolDir is the directory of the files that hold each step's output
// until the gate prints it: "output" in the gate cache, or in the temp
// directory when there is no cache. The gate then holds a few lines per step
// in its own memory, whatever a step prints, so a step's output cannot take
// the gate's reserve in its scope. The directory must be on disk: a file
// system in memory (tmpfs) would charge the output to the gate's memory
// again, so the gate refuses one.
func outputSpoolDir(cacheDir string) (string, error) {
	base := cacheDir
	if base == "" {
		base = filepath.Join(os.TempDir(), "ergo-gate")
	}
	dir := filepath.Join(base, "output")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("output spool: %w", err)
	}
	inMemory, err := memoryFileSystem(dir)
	if err != nil {
		return "", fmt.Errorf("output spool %s: %w", dir, err)
	}
	if inMemory {
		return "", fmt.Errorf("output spool %s is on a file system in memory, which would hold step output in the gate's memory; set ERGO_GATE_CACHE to a directory on disk", dir)
	}
	return dir, nil
}

// newOutputSpool is an empty file in dir for one step's output. It is
// unlinked at once, so it leaves nothing behind however the gate ends.
func newOutputSpool(dir string) (*os.File, error) {
	file, err := os.CreateTemp(dir, "step-")
	if err != nil {
		return nil, err
	}
	if err := os.Remove(file.Name()); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}
