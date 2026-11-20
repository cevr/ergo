package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/syntax"
)

// TestNewSyntaxOnTheSameLayoutTakesTwoStages: a std source that uses
// syntax the linked parser refuses (a new token, the node layout
// unchanged) makes the tree one that takes two stages. refused holds back
// exactly the refused line and writes nothing; -bootstrap then runs two
// stages; a plain run is refused with the -bootstrap instruction. A tree
// the linked parser reads holds nothing back and regenerates once. The
// parser is the linked one, in process, as scripts/linked runs it.
//
// Mutant: linked-refusals-ignored (stages decides by the layout alone).
func TestNewSyntaxOnTheSameLayoutTakesTwoStages(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"internal/sketch/std/p", "compiler"} {
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(dir)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(root, "internal", "sketch", "std", "p", "p.ergo")
	write := func(src string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write("func one() int {\n\t1\n}\n")
	held, err := refused(root)
	if err != nil {
		t.Fatal(err)
	}
	if two, err := stages(syntax.ColumnsVersion, held, true, false); err != nil || two || len(held) != 0 {
		t.Fatalf("a tree the linked parser reads: held %v, two stages %v, %v; want none, once", held, two, err)
	}

	src := "func one() int {\n\t1\n}\n\nfunc two() int {\n\t1 ⊕ 2\n}\n"
	write(src)
	held, err = refused(root)
	if err != nil {
		t.Fatal(err)
	}
	h, ok := held[path]
	if len(held) != 1 || !ok {
		t.Fatalf("held %v; want only %s", held, path)
	}
	want := strings.Replace(src, "\t1 ⊕ 2", heldMark+"\t1 ⊕ 2", 1)
	if h.orig != src || h.src != want {
		t.Errorf("held back:\n%s\nwant:\n%s", h.src, want)
	}
	if disk, err := os.ReadFile(path); err != nil || string(disk) != src {
		t.Errorf("refused wrote the source: %q, %v", disk, err)
	}
	if two, err := stages(syntax.ColumnsVersion, held, true, false); err != nil || !two {
		t.Errorf("-bootstrap on a tree with a refused line: two stages %v, %v; want two", two, err)
	}
	if _, err := stages(syntax.ColumnsVersion, held, false, false); err == nil || !strings.Contains(err.Error(), "-bootstrap") {
		t.Errorf("a plain run on a tree with a refused line: %v; want the -bootstrap instruction", err)
	}
	if two, err := stages(syntax.ColumnsVersion, map[string]heldFile{}, false, true); err != nil || two {
		t.Errorf("stage 1: two stages %v, %v; want once", two, err)
	}
}
