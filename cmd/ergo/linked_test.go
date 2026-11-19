package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/linked"
)

// TestLinkedGoIsFresh: every file the generation of each linked package
// (internal/linked) writes is checked in with the bytes Generate writes
// now, and the generated folders hold no other file. An edit of a linked
// module, of a std package it imports, or of the lowering that leaves the
// checked-in Go behind fails here until `go run ./scripts/linked` has run.
// It lives in cmd/ergo, the binary that links the packages, and runs in
// the full tier. Mutant
// linked-cli-not-written (testdata/mutants.json).
func TestLinkedGoIsFresh(t *testing.T) {
	if testing.Short() {
		// The fast tier's CPU budget has no headroom (scripts/gate
		// fastCPUKnownGap); the full tier runs it on every batch.
		t.Skip("full tier: regenerates every linked package")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range linked.Packages {
		files, err := linked.Generate(root, p)
		if err != nil {
			t.Fatal(err)
		}
		for _, rel := range linked.Sorted(files) {
			have, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
			if err != nil || !bytes.Equal(have, files[rel]) {
				t.Errorf("%s is stale: run go run ./scripts/linked", rel)
			}
		}
		dirs := []string{p.Dir()}
		for _, c := range p.CLIs {
			dirs = append(dirs, p.Dir()+"/ergo/"+c.Dir)
		}
		for _, dir := range dirs {
			entries, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(dir)))
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range entries {
				if e.IsDir() || strings.HasSuffix(e.Name(), "_test.go") {
					continue
				}
				if _, ok := files[dir+"/"+e.Name()]; !ok {
					t.Errorf("%s/%s is no file the generation of %s writes: remove it", dir, e.Name(), p.Name)
				}
			}
		}
	}
}
