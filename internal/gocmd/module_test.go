package gocmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestErgoPackagesOneLanguagePerPackage: the module's Ergo packages are
// its folders of .ergo files, as ./... walks them, and a folder that also
// holds a Go file of its own package is refused with the owner's rule
// (q296: package by package, no mixed packages), while a Go test of
// package NAME_test beside the .ergo files is Go importing the package.
// It checks in process; the refusal's text is the row.
//
// Mutants: gocmd-mixed-allowed (a Go file beside .ergo files is taken),
// gocmd-xtest-refused (a NAME_test file is refused), gocmd-testdata-walked
// (testdata is an Ergo package).
func TestErgoPackagesOneLanguagePerPackage(t *testing.T) {
	root := t.TempDir()
	write := func(rel, text string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.com/mix\n\ngo 1.27\n")
	write("lib/lib.ergo", "func Add(a int, b int) int { a + b }\n")
	write("lib/lib_test.go", "package lib_test\n")
	write("lib/testdata/x.ergo", "func X() int { 1 }\n")
	write("calc/calc.go", "package calc\n")
	m, found, err := FindModule(filepath.Join(root, "calc"))
	if err != nil || !found || m.Path != "example.com/mix" || m.Root != root {
		t.Fatalf("FindModule = %+v %v %v", m, found, err)
	}
	pkgs, err := ErgoPackages(m)
	if err != nil {
		t.Fatal(err)
	}
	if len(pkgs) != 1 || pkgs[0].ImportPath != "example.com/mix/lib" || pkgs[0].Name != "lib" || pkgs[0].Dir != filepath.Join(root, "lib") {
		t.Fatalf("ErgoPackages = %+v, want example.com/mix/lib alone", pkgs)
	}
	write("lib/extra.go", "package lib\n")
	_, err = ErgoPackages(m)
	if err == nil || !strings.Contains(err.Error(), "package example.com/mix/lib holds both .ergo files (lib.ergo) and the Go file extra.go: one package is one language (owner q296)") {
		t.Fatalf("a Go file beside lib.ergo: %v, want the one-language refusal", err)
	}
}
