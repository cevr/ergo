package main

import (
	"os"
	"testing"
)

// An output spool is unlinked as it is made, so it leaves nothing behind
// however the gate ends.
func TestOutputSpoolIsUnlinked(t *testing.T) {
	t.Parallel()
	dir, err := outputSpoolDir(t.TempDir())
	if err != nil {
		t.Skipf("the test temp directory cannot hold a spool: %v", err)
	}
	spool, err := newOutputSpool(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer spool.Close()
	if _, err := spool.WriteString("output\n"); err != nil {
		t.Fatal(err)
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Fatalf("spool directory holds %v, %v; want nothing", entries, err)
	}
}
