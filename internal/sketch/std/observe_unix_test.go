//go:build unix

package std

import (
	"os"
	"path"
	"path/filepath"
	"syscall"
	"testing"
)

// Under a coverage record (ObserveVariable) each embedded file Package and
// Prelude read is opened at its checkout path, which the test log of the
// reading test names (gate-speed GS9). The checkout copy here is a FIFO: the
// writer's open returns only once observe opened it for reading, an event,
// so a missing open hangs until the test's deadline names it. Mutants it
// catches, which no other test catches (std's testdata/mutants.json):
// gs9-observe-never (observe opens nothing) and gs9-package-unobserved
// (Package reads a file without observing it).
func TestObserveOpensTheCheckoutCopy(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, filepath.FromSlash(sourceDir), "json")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	files, ok := Package("json")
	if !ok || len(files) == 0 {
		t.Fatal("no std package json")
	}
	var first string
	for name := range files {
		if first == "" || name < first {
			first = name
		}
	}
	fifo := filepath.Join(dir, first)
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("no FIFO here: %v", err)
	}
	t.Setenv(ObserveVariable, root)
	opened := make(chan error, 1)
	go func() {
		f, err := os.OpenFile(fifo, os.O_WRONLY, 0)
		if err == nil {
			f.Close()
		}
		opened <- err
	}()
	if again, ok := Package("json"); !ok || again[first] != files[first] {
		t.Fatalf("Package(%q) under a record read other bytes", "json")
	}
	if err := <-opened; err != nil {
		t.Fatalf("open %s for writing: %v", path.Join("json", first), err)
	}
	prelude := filepath.Join(root, filepath.FromSlash(sourceDir), "prelude.ergo")
	if err := syscall.Mkfifo(prelude, 0o600); err != nil {
		t.Fatal(err)
	}
	go func() {
		f, err := os.OpenFile(prelude, os.O_WRONLY, 0)
		if err == nil {
			f.Close()
		}
		opened <- err
	}()
	Prelude()
	if err := <-opened; err != nil {
		t.Fatalf("open prelude.ergo for writing: %v", err)
	}
}
