//go:build unix

package gostd

import (
	"os"
	"path"
	"path/filepath"
	"syscall"
	"testing"
)

// Under a coverage record (observeVariable) each binding Binding reads is
// opened at its checkout path, which the test log of the reading test names
// (the gate's GS9 record and the mutant runner's coverage pass, GT8b). The
// checkout copy here is a FIFO: the writer's open returns only once observe
// opened it for reading, an event, so a missing open hangs until the test's
// deadline names it. It catches gt8b-binding-unobserved (internal/sketch/
// stdlib's ledger: Binding reads a binding without observing it), which no
// other test catches: stdlib's TestObserveOpensTheCheckoutCopy reads std's
// sources, not gostd's bindings.
func TestObserveOpensTheBinding(t *testing.T) {
	importPath := Paths()[0]
	name := path.Join("src", importPath, path.Base(importPath)+".ergo")
	want, ok := Binding(importPath)
	if !ok {
		t.Fatalf("no binding of %s", importPath)
	}
	root := t.TempDir()
	fifo := filepath.Join(root, filepath.FromSlash(sourceDir), filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(fifo), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("no FIFO here: %v", err)
	}
	t.Setenv(observeVariable, root)
	opened := make(chan error, 1)
	go func() {
		f, err := os.OpenFile(fifo, os.O_WRONLY, 0)
		if err == nil {
			f.Close()
		}
		opened <- err
	}()
	if got, ok := Binding(importPath); !ok || got != want {
		t.Fatalf("Binding(%q) under a record read other bytes", importPath)
	}
	if err := <-opened; err != nil {
		t.Fatalf("open %s for writing: %v", name, err)
	}
}
