// Package gostd embeds the Ergo bindings of Go's standard library, one
// generated file per package under src, made by ergo bind go at the pinned
// toolchain (docs/spec/interop.md, Go's standard library). The checker
// reads them as files; it never runs a Go tool. TestGoStdBindingsMatchToolchain
// regenerates them and compares.
package gostd

import (
	"embed"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

//go:generate go run ergo.local/prototype/internal/gobind/gostdgen src

//go:embed src
var bindings embed.FS

// observeVariable is internal/sketch/stdlib's ObserveVariable: when set,
// the checkout root of a coverage record (the gate's, gate-speed GS9, or
// the mutant runner's coverage pass, GT8b). Each binding Binding reads is
// then also opened at its checkout path, so the test log of the test that
// read it names it as a read. Unset, nothing is opened.
const observeVariable = "ERGO_EMBED_OBSERVE"

// sourceDir is this package's directory in the module.
const sourceDir = "internal/sketch/std/gostd"

// observe opens the checkout copy of the embedded file name under a
// coverage record (observeVariable).
func observe(name string) {
	root := os.Getenv(observeVariable)
	if root == "" {
		return
	}
	if f, err := os.Open(filepath.Join(root, filepath.FromSlash(sourceDir), filepath.FromSlash(name))); err == nil {
		f.Close()
	}
}

// Binding returns the binding file of the Go standard-library package at
// importPath, and whether it has one.
func Binding(importPath string) (string, bool) {
	if importPath == "" || strings.Contains(importPath, "..") {
		return "", false
	}
	name := path.Join("src", importPath, path.Base(importPath)+".ergo")
	data, err := bindings.ReadFile(name)
	if err != nil {
		return "", false
	}
	observe(name)
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
