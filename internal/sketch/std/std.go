// Package std holds the Ergo source of the language's prelude and
// standard-library root (docs/spec/stdlib.md). The sources are ordinary
// Ergo code: internal/sketch/check checks them with the same rules as user
// packages, so no library item is a compiler branch (owner, 2026-10-12;
// GLOSSARY, special-cased library). #206 adds packages here without checker
// changes.
//
// Prior art: MoonBit and OCaml ship their standard library as source that
// the same compiler checks; Gleam keeps its prelude in the compiler
// (compiler-core/src/type_/prelude.rs), which Ergo does not copy.
package std

import (
	"embed"
	"io/fs"
	"path"
	"sort"
	"strings"
)

//go:embed prelude.ergo timeout/*.ergo task/*.ergo scope/*.ergo slice/*.ergo queue/*.ergo log/*.ergo trace/*.ergo strings/*.ergo strconv/*.ergo
//go:embed os/*.ergo
//go:embed fs/*.ergo
//go:embed io/*.ergo
var sources embed.FS

// PreludePath is the package path of the prelude. No import names it: its
// exported declarations are in scope in every package.
const PreludePath = "prelude"

// Prelude returns the prelude's source files, by file name.
func Prelude() map[string]string {
	data, err := sources.ReadFile("prelude.ergo")
	if err != nil {
		panic("std: the prelude is not embedded: " + err.Error())
	}
	return map[string]string{"prelude.ergo": string(data)}
}

// Package returns the source files of the standard-library package at the
// import path, by file name, and whether the path names one. The prelude is
// not importable.
func Package(importPath string) (map[string]string, bool) {
	if importPath == "" || importPath == PreludePath || importPath == "." || strings.Contains(importPath, "..") {
		return nil, false
	}
	entries, err := fs.ReadDir(sources, importPath)
	if err != nil {
		return nil, false
	}
	files := map[string]string{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".ergo") {
			continue
		}
		data, err := sources.ReadFile(path.Join(importPath, entry.Name()))
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
	_ = fs.WalkDir(sources, ".", func(p string, d fs.DirEntry, err error) error {
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
