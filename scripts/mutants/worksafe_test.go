package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCheckoutInsideWorkIsRefused: a checkout inside the work directory
// could be one of its copies, which copyTree empties first and
// restoreUnder prunes, so the runner refuses it before it writes anything,
// and the checkout keeps its files. Mutant: the check is dropped
// (work-contains-checkout).
func TestCheckoutInsideWorkIsRefused(t *testing.T) {
	work := t.TempDir()
	root := filepath.Join(work, "w0", "src")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	mod := filepath.Join(root, "go.mod")
	if err := os.WriteFile(mod, []byte("module fake.test/m\n\ngo 1.27\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := Run(context.Background(), Config{Root: root, Work: work, Workers: 1}, nil)
	if err == nil || !strings.Contains(err.Error(), "inside the work directory") {
		t.Fatalf("a checkout inside the work directory: %v", err)
	}
	if _, err := os.Stat(mod); err != nil {
		t.Fatalf("the checkout lost its go.mod: %v", err)
	}
}
