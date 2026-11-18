package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketchfront"
)

// TestDiagnosticCases checks the cases of testdata/diagnostics: each is a
// package that a module of the repository refuses, with the diagnostics
// the checker must give. A case folder holds `module` (the module the
// package joins, a path from the repository root), the package's .ergo
// files, and `expected` (one diagnostic a line: `FILE:LINE:COLUMN: CODE
// MESSAGE`). The test copies the module's .ergo files and ergo.mod into a
// temporary tree, adds the package as the folder named after the case, and
// checks it in process. These rows are concrete because they pin a
// diagnostic's text and position (AGENTS.md, Tests).
//
// source-not-wire (wf10 RPX1) is a sync source whose state holds a server
// handle: replica.Source requires its state's codec (`S wire`), so the
// checker refuses it at the Source and names the field. It catches
// rpx1-source-without-codec (the constraint removed: the case checks).
//
// web-failure-without-status (wf10 SSR WEB1) is a site whose loader can
// fail with a variant its status table (modules/web Site.Status) has no
// case for: the site does not compile, so no typed failure reaches a
// user as a 200 or a bare 500. It catches web1-status-case-wildcard (the
// table answers every other failure with `case _`).
func TestDiagnosticCases(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	cases, err := os.ReadDir("testdata/diagnostics")
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) == 0 {
		t.Fatal("no case in testdata/diagnostics")
	}
	for _, c := range cases {
		t.Run(c.Name(), func(t *testing.T) {
			dir := filepath.Join("testdata/diagnostics", c.Name())
			module, err := os.ReadFile(filepath.Join(dir, "module"))
			if err != nil {
				t.Fatal(err)
			}
			want, err := os.ReadFile(filepath.Join(dir, "expected"))
			if err != nil {
				t.Fatal(err)
			}
			tree := t.TempDir()
			copyModule(t, filepath.Join(root, strings.TrimSpace(string(module))), tree)
			pkg := filepath.Join(tree, c.Name())
			if err := os.MkdirAll(pkg, 0o755); err != nil {
				t.Fatal(err)
			}
			files, err := filepath.Glob(filepath.Join(dir, "*.ergo"))
			if err != nil || len(files) == 0 {
				t.Fatalf("no .ergo file in %s (%v)", dir, err)
			}
			for _, f := range files {
				text, err := os.ReadFile(f)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(pkg, filepath.Base(f)), text, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			a, err := sketchfront.CheckDir(pkg)
			if err != nil {
				t.Fatal(err)
			}
			var got strings.Builder
			for _, d := range a.Diagnostics {
				fmt.Fprintf(&got, "%s:%d:%d: %s %s\n", d.File, d.Span.Line, d.Span.Column, d.Code, d.Message)
			}
			if got.String() != string(want) {
				t.Errorf("diagnostics of %s:\n%s\nwant:\n%s", c.Name(), got.String(), want)
			}
		})
	}
}

// copyModule copies the ergo.mod and the .ergo files of the module at from
// into to, folder by folder.
func copyModule(t *testing.T, from, to string) {
	t.Helper()
	err := filepath.WalkDir(from, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(from, path)
		if err != nil {
			return err
		}
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(to, rel), 0o755)
		}
		if d.Name() != "ergo.mod" && !strings.HasSuffix(d.Name(), ".ergo") {
			return nil
		}
		text, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(to, rel), text, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}
