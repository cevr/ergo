package build

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDirectCompileKeysOnTheIR: a package's compile on the direct path is
// keyed by the content of the IR file in its folder, the patched compile's
// -ergoir input, since its Go file carries no digest of it: the same
// main.go with other IR bodies is another compile, and another package
// does not read that IR (CS10: one IR file per package). Mutants: the key
// leaves the IR out (irt-direct-key-without-ir); every package's key
// holds the root's IR (irt-ir-key-every-package).
func TestDirectCompileKeysOnTheIR(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "main.go")
	if err := os.WriteFile(main, []byte("package main\n\nfunc f() int\n\nfunc main() { _ = f() }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(dir, "x")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	x := filepath.Join(sub, "x.go")
	if err := os.WriteFile(x, []byte("package x\n\nfunc G() int { return 1 }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	d := &direct{module: Module{Dir: dir}, plan: planFile{mainPath: "ergo.local/program"}}
	root := &goPackage{ImportPath: "ergo.local/program", Dir: dir, Files: []string{main}, Trim: "ergo.local/program"}
	other := &goPackage{ImportPath: "ergo.local/program/x", Dir: sub, Files: []string{x}, Trim: "ergo.local/program/x"}
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
