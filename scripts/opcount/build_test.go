package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/opcount"
	"ergo.local/prototype/internal/toolchain"
)

// write puts each file (path relative to dir) into dir.
func write(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, data := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// The program builds as the product does: ergo's patched release, and
// every package with an IR file compiled with its own -ergoir.
func TestModuleBuildReadsEachPackagesIR(t *testing.T) {
	// The build keeps the caller's GOFLAGS first (PARK1); the gate sets
	// its own, so the expected line is checked with none.
	t.Setenv("GOFLAGS", "")
	dir := t.TempDir()
	write(t, dir, map[string]string{
		"go.mod":            "module ergo.local/program\n\ngo 1.27\n",
		"main.go":           "package main\n",
		"ergoir.json":       `{"funcs":[]}`,
		"pkg/a/a.go":        "package a\n",
		"pkg/a/ergoir.json": `{"funcs":[]}`,
		"pkg/b/b.go":        "package b\n",
	})
	release := toolchain.Patched{Root: "/patched/goroot", Go: "/patched/goroot/bin/go"}
	cmd := moduleBuild(release, dir, "/scratch/overlay.json", "/scratch/bin", "")
	if cmd.Args[0] != release.Go {
		t.Errorf("go command %q, want the patched release's %q", cmd.Args[0], release.Go)
	}
	// The IR file is named relative to the module (GP-SPLIT-EDIT: Go's
	// action cache keys the compile on the flag), which holds because go
	// build runs in the module's folder and runs each compile there.
	if cmd.Dir != dir {
		t.Errorf("go build runs in %q, want the module's folder %q, where its relative -ergoir paths resolve", cmd.Dir, dir)
	}
	for _, want := range []string{
		"-gcflags=.='-ergoir=./ergoir.json'",
		"-gcflags=./pkg/a='-ergoir=./pkg/a/ergoir.json'",
	} {
		if !slices.Contains(cmd.Args, want) {
			t.Errorf("args %q lack %s", cmd.Args, want)
		}
	}
	if got := strings.Join(cmd.Args, " "); strings.Contains(got, "pkg/b") {
		t.Errorf("args %q give pkg/b, which has no IR file, an -ergoir", got)
	}
	env := strings.Join(cmd.Env, "\n")
	for _, want := range []string{"GOROOT=" + release.Root, "GOFLAGS='-overlay=/scratch/overlay.json' -ldflags=-checklinkname=0"} {
		if !strings.Contains(env, want+"\n") && !strings.HasSuffix(env, want) {
			t.Errorf("environment lacks %s", want)
		}
	}
}

// A package's count is the functions its IR file names against its
// function declarations; the runtime's nested module is not counted.
func TestIRBodiesPerPackage(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, map[string]string{
		"go.mod":              "module ergo.local/program\n",
		"main.go":             "package main\n\ntype T struct{}\n\nfunc f()\nfunc g() {}\nfunc (T) m()\n",
		"ergoir.json":         `{"v":1,"funcs":[{"name":"f"},{"name":"T.m"}]}`,
		"main_test.go":        "package main\n\nfunc h() {}\n",
		"pkg/p/p.go":          "package p\n\nfunc P() {}\n",
		"ergo/go.mod":         "module ergo\n",
		"ergo/runtime/r/r.go": "package r\n\nfunc R() {}\n",
	})
	got, err := irBodies(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []opcount.Bodies{{Package: ".", IR: 2, Funcs: 3}, {Package: "pkg/p", IR: 0, Funcs: 1}}
	if !slices.Equal(got, want) {
		t.Errorf("bodies %+v, want %+v", got, want)
	}
}
