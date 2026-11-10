// Package gostd embeds the Ergo bindings of Go's standard library, one
// generated file per package under src, made by ergo bind go at the pinned
// toolchain (docs/spec/interop.md, Go's standard library). The checker
// reads them as files; it never runs a Go tool. TestGoStdBindingsMatchToolchain
// regenerates them and compares.
package gostd

import (
	"embed"
	"io/fs"
	"path"
	"sort"
	"strings"
)

//go:generate go run ergo.local/prototype/internal/gobind/gostdgen src

//go:embed src
var bindings embed.FS

// Binding returns the binding file of the Go standard-library package at
// importPath, and whether it has one.
func Binding(importPath string) (string, bool) {
	if importPath == "" || strings.Contains(importPath, "..") {
		return "", false
	}
	data, err := bindings.ReadFile(path.Join("src", importPath, path.Base(importPath)+".ergo"))
	if err != nil {
		return "", false
	}
	return string(data), true
}

// Paths lists the import paths that have a binding, sorted.
func Paths() []string {
	var out []string
	_ = fs.WalkDir(bindings, "src", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".ergo") {
			dir := path.Dir(strings.TrimPrefix(p, "src/"))
			if path.Base(dir)+".ergo" == path.Base(p) {
				out = append(out, dir)
			}
		}
		return nil
	})
	sort.Strings(out)
	return out
}
