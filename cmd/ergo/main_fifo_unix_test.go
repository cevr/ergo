//go:build unix

package main

import (
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fifoBound is how long a refusal of a FIFO may take. An open of the FIFO
// blocks for good, so any bound tells the two apart; 1 s could fail a
// loaded host (fu-gate-flakes).
const fifoBound = 30 * time.Second

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
	case <-time.After(fifoBound):
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
	case <-time.After(fifoBound):
		t.Fatal("formatter source admission blocked before opening FIFO")
	}
}
