// Package stdlib holds the Ergo source of the language's prelude and
// standard-library root (docs/spec/stdlib.md). The sources are ordinary
// Ergo code: internal/sketch/check checks them with the same rules as user
// packages, so no library item is a compiler branch (owner, 2026-10-12;
// GLOSSARY, special-cased library). #206 adds packages here without checker
// changes.
//
// Prior art: MoonBit and OCaml ship their standard library as source that
// the same compiler checks; Gleam keeps its prelude in the compiler
// (compiler-core/src/type_/prelude.rs), which Ergo does not copy.
package stdlib

import (
	"ergo.local/prototype/internal/sketch/std"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// Ledger is the std law ledger (runtime.md, Tests 10): the verdicts of
// std's rewrite laws, written by `go run ./scripts/stdlaws` and read by
// every build like a module's ergo.laws (std.LedgerText, embedded beside
// the sources).
func Ledger() string { return std.LedgerText }

// PreludePath is the package path of the prelude. No import names it: its
// exported declarations are in scope in every package.
const PreludePath = "prelude"

// ObserveVariable names, when set, the checkout root of a coverage record
// (scripts/gate, gate-speed GS9): each embedded file a caller reads is then
// also opened at its checkout path, so the test log of the test that read it,
// or of the test whose child process read it, names it as a read. The gate's
// test-impact selection then reruns only the readers of an edited std file,
// not every test. Unset, nothing is opened.
const ObserveVariable = "ERGO_EMBED_OBSERVE"

// sourceDir is this package's directory in the module.
const sourceDir = "internal/sketch/std"

// observe opens the checkout copy of the embedded file name under a coverage
// record (ObserveVariable).
var observe = func(name string) {
	root := os.Getenv(ObserveVariable)
	if root == "" {
		return
	}
	if f, err := os.Open(filepath.Join(root, filepath.FromSlash(sourceDir), filepath.FromSlash(name))); err == nil {
		f.Close()
	}
}

// Prelude returns the prelude's source files, by file name: the .ergo
// files of the std root, prelude.ergo and the language's operations,
// builtin.ergo.
func Prelude() map[string]string {
	entries, err := fs.ReadDir(std.Sources, ".")
	if err != nil {
		panic("std: the prelude is not embedded: " + err.Error())
	}
	files := map[string]string{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".ergo") {
			continue
		}
		observe(entry.Name())
		data, err := std.Sources.ReadFile(entry.Name())
		if err != nil {
			panic("std: the prelude is not embedded: " + err.Error())
		}
		files[entry.Name()] = string(data)
	}
	return files
}

// Package returns the source files of the standard-library package at the
// import path, by file name, and whether the path names one. The prelude is
// not importable.
func Package(importPath string) (map[string]string, bool) {
	if importPath == "" || importPath == PreludePath || importPath == "." || strings.Contains(importPath, "..") {
		return nil, false
	}
	entries, err := fs.ReadDir(std.Sources, importPath)
	if err != nil {
		return nil, false
	}
	files := map[string]string{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".ergo") {
			continue
		}
		observe(path.Join(importPath, entry.Name()))
		data, err := std.Sources.ReadFile(path.Join(importPath, entry.Name()))
		if err != nil {
			return nil, false
		}
		files[entry.Name()] = string(data)
	}
	if len(files) == 0 {
		return nil, false
	}
	return files, true
}

// Packages lists the importable standard-library paths, sorted.
func Packages() []string {
	var out []string
	_ = fs.WalkDir(std.Sources, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() || p == "." {
			return nil
		}
		if _, ok := Package(p); ok {
			out = append(out, p)
		}
		return nil
	})
	sort.Strings(out)
	return out
}
