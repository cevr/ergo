package linked

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLinkedGoIsFresh: every file the generation of each linked package
// writes is checked in with the bytes Generate writes now, and the
// generated folders hold no other file. An edit of a linked module, of a
// std package it imports, or of the lowering that leaves the checked-in
// Go behind fails here until `go run ./scripts/linked` has run. Mutant
// linked-go-hand-edit (testdata/mutants.json).
func TestLinkedGoIsFresh(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range Packages {
		files, err := Generate(root, p)
		if err != nil {
			t.Fatal(err)
		}
		for _, rel := range Sorted(files) {
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
				rel := dir + "/" + e.Name()
				if e.IsDir() || strings.HasSuffix(e.Name(), "_test.go") {
					continue
				}
				if _, ok := files[rel]; !ok {
					t.Errorf("%s is no file the generation of %s writes: remove it", rel, p.Name)
				}
			}
		}
	}
}
