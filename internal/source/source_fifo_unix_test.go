//go:build unix

package source

import (
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"ergo.local/prototype/internal/testwait"
)

func TestReadRegularFileRejectsFIFOBeforeOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "blocked.ergo")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}

	result := make(chan error, 1)
	go func() {
		_, err := ReadRegularFile(path, 0)
		result <- err
	}()

	select {
	case err := <-result:
		if err == nil || !strings.Contains(err.Error(), "source must be a regular file") {
			t.Fatalf("unexpected FIFO admission result: %v", err)
		}
	case <-testwait.After(t):
		t.Fatal("regular-file admission blocked before opening FIFO")
	}
}

// Open has no stat before the open, as when a FIFO replaces the file after a
// caller's stat: the non-blocking open returns at once and the FIFO is then
// refused.
func TestOpenRefusesFIFOWithoutBlocking(t *testing.T) {
	path := filepath.Join(t.TempDir(), "blocked.ergo")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		file, err := Open(path)
		if err == nil {
			file.Close()
		}
		result <- err
	}()
	select {
	case err := <-result:
		if err == nil || !strings.Contains(err.Error(), "source must be a regular file") {
			t.Fatalf("unexpected FIFO admission result: %v", err)
		}
	case <-testwait.After(t):
		t.Fatal("Open blocked on a FIFO")
	}
}
