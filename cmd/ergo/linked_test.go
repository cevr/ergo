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
// every tier, the fast one too: it skipped under -short, and main's
// code.go went stale through batch 22 unnoticed (SELFHOST-MERGE,
// codego-stale.md). It regenerates only on a miss of the stamp
// (linked.Fresh): while the generation's inputs digest is the one
// `go run ./scripts/linked` stamped, the files are fresh when they hash
// as stamped. Mutants linked-source-ignored, linked-exports-all,
// linked-ir-ignored, library-reach-main-only, linked-cli-not-written and
// linked-gofmt-one-pass (testdata/mutants.json); the stamp's own mutants
// are internal/linked's.
func TestLinkedGoIsFresh(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	read := func(rel string) ([]byte, error) { return os.ReadFile(filepath.Join(root, filepath.FromSlash(rel))) }
	stamp, _ := read(linked.StampFile)
	inputs, err := linked.InputsDigest(root)
	if err != nil {
		t.Fatal(err)
	}
	fresh := linked.Fresh(stamp, inputs, read)
	if !fresh {
		t.Logf("%s does not vouch for the generated files (inputs changed or a file differs): regenerating", linked.StampFile)
	}
	for _, p := range linked.Packages {
		var files map[string][]byte
		if !fresh {
			files, err = linked.Generate(root, p)
			if err != nil {
				t.Fatal(err)
			}
			for _, rel := range linked.Sorted(files) {
				have, err := read(rel)
				if err != nil || !bytes.Equal(have, files[rel]) {
					t.Errorf("%s is stale: run go run ./scripts/linked", rel)
				}
			}
		} else {
			files = stampedFiles(stamp)
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

// stampedFiles is the set of paths a stamp lists.
func stampedFiles(stamp []byte) map[string][]byte {
	out := map[string][]byte{}
	lines := strings.Split(strings.TrimSuffix(string(stamp), "\n"), "\n")
	for _, line := range lines[1:] {
		if _, rel, ok := strings.Cut(line, " "); ok {
			out[rel] = nil
		}
	}
	return out
}
