package build

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDirectCompileKeysOnTheIR: the root package's compile on the direct
// path is keyed by the IR file's content, the patched compile's -ergoir
// input, since main.go carries no digest of it: the same main.go with
// other IR bodies is another compile, and a package that is not the root
// does not read the IR. Mutant: the key leaves the IR out
// (irt-direct-key-without-ir).
func TestDirectCompileKeysOnTheIR(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "main.go")
	if err := os.WriteFile(main, []byte("package main\n\nfunc f() int\n\nfunc main() { _ = f() }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	d := &direct{module: Module{Dir: dir}, plan: planFile{mainPath: "ergo.local/program"}}
	root := &goPackage{ImportPath: "ergo.local/program", Dir: dir, Files: []string{main}, Trim: "ergo.local/program"}
	other := &goPackage{ImportPath: "ergo.local/program/x", Dir: dir, Files: []string{main}, Trim: "ergo.local/program/x"}
	keys := func() (string, string) {
		t.Helper()
		a, err := d.compileKey(root)
		if err != nil {
			t.Fatal(err)
		}
		b, err := d.compileKey(other)
		if err != nil {
			t.Fatal(err)
		}
		return a, b
	}
	write := func(ir string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, IRFile), []byte(ir), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"v":1,"funcs":[{"name":"f","body":1}]}`)
	rootA, otherA := keys()
	write(`{"v":1,"funcs":[{"name":"f","body":2}]}`)
	rootB, otherB := keys()
	if rootA == rootB {
		t.Errorf("the root compile's key is %s for both IR files", rootA)
	}
	if otherA != otherB {
		t.Errorf("a non-root package's key moved with the IR: %s, %s", otherA, otherB)
	}
}
