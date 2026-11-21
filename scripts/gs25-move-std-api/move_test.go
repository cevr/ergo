package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCheckoutKeepsStdALeaf: in this checkout nothing is left to move, so
// a std edit recompiles only std and stdlib (GS25). A lane that imports
// internal/sketch/std again, or adds a Go file or an embed line to it, fails
// here; `go run ./scripts/gs25-move-std-api` moves it.
func TestCheckoutKeepsStdALeaf(t *testing.T) {
	m := &mover{root: filepath.Join("..", ".."), check: true}
	if err := m.run(); err != nil {
		t.Fatal(err)
	}
	if len(m.changes) > 0 {
		t.Fatalf("left to move (go run ./scripts/gs25-move-std-api):\n%s", strings.Join(m.changes, "\n"))
	}
}

// TestMoveAppliesToTheOldLayout: on a module in the old layout (std embeds
// its sources and holds their API, a package and std's own test use it)
// the move leaves std with embed.go alone, stdlib reading std.Sources, and
// every user on stdlib; a second run finds nothing left. It catches
// gs25-move-skips-go-files, gs25-embed-lines-kept, gs25-sources-unread,
// gs25-package-unrenamed and gs25-importer-kept.
func TestMoveAppliesToTheOldLayout(t *testing.T) {
	root := t.TempDir()
	put := func(rel, text string) {
		t.Helper()
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	put("go.mod", "module "+module+"\n\ngo 1.24\n")
	put(leafRel+"/a/a.ergo", "func A() int { 1 }\n")
	put(leafRel+"/std.go", "// Package std holds the sources.\npackage std\n\nimport (\n\t\"embed\"\n\t\"strings\"\n)\n\n// sources are embedded.\n//\n//go:embed a/*.ergo\nvar sources embed.FS\n\n// Package returns a file.\nfunc Package(name string) string {\n\tdata, _ := sources.ReadFile(name)\n\treturn strings.TrimSpace(string(data))\n}\n")
	put(leafRel+"/std_test.go", "package std_test\n\nimport (\n\t\"testing\"\n\n\t\""+leafPath+"\"\n)\n\nfunc TestA(t *testing.T) { _ = std.Package(\"a/a.ergo\") }\n")
	put("internal/p/p.go", "package p\n\nimport \""+leafPath+"\"\n\n// P reads std.\nfunc P() string { return std.Package(\"a/a.ergo\") }\n")
	m := &mover{root: root}
	if err := m.run(); err != nil {
		t.Fatal(err)
	}
	read := func(rel string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	entries, err := os.ReadDir(filepath.Join(root, leafRel))
	if err != nil {
		t.Fatal(err)
	}
	var goFiles []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".go") {
			goFiles = append(goFiles, e.Name())
		}
	}
	if strings.Join(goFiles, " ") != embedFile {
		t.Errorf("std's Go files after the move: %v; want only %s", goFiles, embedFile)
	}
	if leaf := read(leafRel + "/" + embedFile); !strings.Contains(leaf, "//go:embed a/*.ergo\nvar Sources embed.FS\n") || !strings.Contains(leaf, "package std\n") {
		t.Errorf("std's embed.go:\n%s", leaf)
	}
	api := read(apiRel + "/std.go")
	for _, want := range []string{"package stdlib\n", "// Package stdlib holds", "std.Sources.ReadFile(name)", "\"" + leafPath + "\"", "\"strings\""} {
		if !strings.Contains(api, want) {
			t.Errorf("stdlib's std.go lacks %q:\n%s", want, api)
		}
	}
	if strings.Contains(api, "go:embed") || strings.Contains(api, "\"embed\"") {
		t.Errorf("stdlib's std.go still embeds:\n%s", api)
	}
	if test := read(apiRel + "/std_test.go"); !strings.Contains(test, "package stdlib_test\n") || !strings.Contains(test, "stdlib.Package(") || !strings.Contains(test, "\""+apiPath+"\"") {
		t.Errorf("the moved test:\n%s", test)
	}
	if p := read("internal/p/p.go"); !strings.Contains(p, "\""+apiPath+"\"") || !strings.Contains(p, "return stdlib.Package(") {
		t.Errorf("the importer after the move:\n%s", p)
	}
	again := &mover{root: root, check: true}
	if err := again.run(); err != nil || len(again.changes) > 0 {
		t.Errorf("a second run: %v, left %q", err, again.changes)
	}
}
