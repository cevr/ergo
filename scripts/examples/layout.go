package main

import (
	"os"
	"path/filepath"
	"strings"
)

// moduleRootFile reports whether file is one the root of every module
// folder may hold, a module of modules/ and a program of packages alike:
// ergo.mod, go.mod and go.sum, the law ledger ergo.laws (read beside
// ergo.mod: runtime.md Tests 10), README.md, expected-skips and the root
// package's .ergo files. Each layout adds its own files to these.
func moduleRootFile(file string) bool {
	switch file {
	case "ergo.mod", "go.mod", "go.sum", "ergo.laws", "README.md", expectedSkipsFile:
		return true
	}
	return strings.HasSuffix(file, ".ergo")
}

// ownGoFile reports whether name, a file of the package folder dir under
// the module folder root, is the module's own Go (interop.md Trusted Go 3;
// LF2m): a Go or assembly file, its tests among them, of a folder whose
// import path the go.mod at root owns. A go.mod with no module line owns
// nothing, and a go.mod between root and dir starts another module, which
// owns the folder instead.
func ownGoFile(root, dir, name string) bool {
	if !strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, ".s") {
		return false
	}
	module, err := moduleName(filepath.Join(root, "go.mod"))
	if err != nil || module == "" {
		return false
	}
	rel, err := filepath.Rel(root, dir)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return false
	}
	for d := dir; d != root; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
			return false
		}
	}
	return true
}
