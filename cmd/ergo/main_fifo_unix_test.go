//go:build unix

package main

import (
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"ergo.local/prototype/internal/testwait"
)

// An open of a FIFO blocks for good, so the test's deadline (testwait)
// tells a refusal from an open; 1 s failed a loaded host (fu-gate-flakes).

func TestLoadRejectsFIFOBeforeOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "blocked.ergo")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}

	result := make(chan error, 1)
	go func() {
		_, err := load(path, "go")
		result <- err
	}()

	select {
	case err := <-result:
		if err == nil || !strings.Contains(err.Error(), "source must be a regular file") {
			t.Fatalf("unexpected CLI FIFO admission result: %v", err)
		}
	case <-testwait.After(t):
		t.Fatal("CLI source admission blocked before opening FIFO")
	}
}

func TestFmtRejectsFIFOBeforeOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "blocked.ergo")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}

	result := make(chan error, 1)
	go func() {
		_, err := formatFiles(fmtOptions{files: []string{path}})
		result <- err
	}()

	select {
	case err := <-result:
		if err == nil || !strings.Contains(err.Error(), "EFMT_SPECIAL_FILE") {
			t.Fatalf("unexpected formatter FIFO admission result: %v", err)
		}
	case <-testwait.After(t):
		t.Fatal("formatter source admission blocked before opening FIFO")
	}
}
