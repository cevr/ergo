package main

import "strings"

// moduleRootFile reports whether file is one the root of every module
// folder may hold, a module of modules/ and a program of packages alike:
// ergo.mod, go.mod and go.sum, the law ledger ergo.laws (read beside
// ergo.mod: runtime.md Tests 10), README.md, expected-skips and the root
// package's .ergo files. Each layout adds its own files to these; which
// Go a module's package folder may hold is moduleGoProblems' rule (a
// folder an extern go of the module binds).
func moduleRootFile(file string) bool {
	switch file {
	case "ergo.mod", "go.mod", "go.sum", "ergo.laws", "README.md", expectedSkipsFile:
		return true
	}
	return strings.HasSuffix(file, ".ergo")
}
