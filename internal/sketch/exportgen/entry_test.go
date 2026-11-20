package exportgen

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestEntryBuildsNoGeneratedPackage holds the regeneration entry,
// scripts/exportgen, to its one promise: its module-local import closure,
// read from the import lines of the non-test files (in process, nothing
// built), holds neither package it generates for. A stale
// codec_access_gen.go breaks package check's build, and with it any
// entry that imports check, the freshness test included; this entry
// still runs. Mutant: exportgen-entry-imports-check.
func TestEntryBuildsNoGeneratedPackage(t *testing.T) {
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	const module = "ergo.local/prototype/"
	seen := map[string]string{} // import path -> who imports it
	queue := []string{"scripts/exportgen"}
	for len(queue) > 0 {
		dir := queue[0]
		queue = queue[1:]
		names, err := filepath.Glob(filepath.Join(root, dir, "*.go"))
		if err != nil || len(names) == 0 {
			t.Fatalf("%s: no Go files (%v)", dir, err)
		}
		for _, name := range names {
			if strings.HasSuffix(name, "_test.go") {
				continue
			}
			f, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.ImportsOnly)
			if err != nil {
				t.Fatal(err)
			}
			for _, spec := range f.Imports {
				p, _ := strconv.Unquote(spec.Path.Value)
				rel, ok := strings.CutPrefix(p, module)
				if !ok || seen[p] != "" {
					continue
				}
				seen[p] = dir
				queue = append(queue, rel)
			}
		}
	}
	for _, p := range []string{checkPath, syntaxPath} {
		if by := seen[p]; by != "" {
			t.Errorf("scripts/exportgen reaches %s (imported by %s): a stale generated file there stops the regeneration", p, by)
		}
	}
}
