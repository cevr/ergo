package main

import (
	"os"
	"path/filepath"
	"testing"
)

// A row's Go killer is keyed on its package's closure also when the runner
// runs with a relative -root (the default "."). The bug (GT2 receipt): the
// module listing compared go list's absolute directories with the relative
// root, found no package, and keyed every row on every file, so an edit
// anywhere judged every row again. Mutant gt2-relative-root.
func TestKeysListARelativeRoot(t *testing.T) {
	root := t.TempDir()
	for name, text := range map[string]string{"go.mod": "module example.test/m\n\ngo 1.24\n", "p/p.go": "package p\n", "p/p_test.go": "package p\n\nimport \"testing\"\n\nfunc TestP(t *testing.T) {}\n", "internal/closure/closure_test.go": "package closure\n\nvar declared = map[string][]allowance{\"p\": {{\"d\", subtree, \"why\"}}}\n"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(filepath.Join(root, "p"))
	listed, err := listModule("..")
	if err != nil {
		t.Fatal(err)
	}
	if c := listed.testClosure("p"); c == nil || !c.Covers("p/p.go") || !c.Covers("d/x") || c.Covers("other/x.go") {
		t.Errorf("the closure of p from the root \"..\" is %+v; want p's own files and its declared d, nothing else", c)
	}
}
