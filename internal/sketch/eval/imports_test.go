package eval_test

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// TestEvalSharesNoLowering is ORACLE O4's import guard: the evaluator is
// the reference every build agrees with, so its product code (the .go
// files of internal/sketch/eval and the packages under it that are not
// tests) reaches, directly or through the module's other packages, no
// package of the lowering (internal/sketch/lower, internal/sketch/ir), and
// imports runtime/v1 itself nowhere: its natives and conversions are held
// against runtime/v1's bodies, so they must not be them (the checker,
// which it imports, reads which v1 bodies take hidden arguments). A test file
// may import them; it is not the evaluator. It reads the files' imports in
// process (go/parser), following the module's own packages.
//
// Mutant only it kills (testdata/mutants.json): oracle-o4-imports-lowering.
func TestEvalSharesNoLowering(t *testing.T) {
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	const module = "ergo.local/prototype/"
	forbidden := []string{module + "internal/sketch/lower", module + "internal/sketch/ir", module + "runtime/v1"}
	// via is, for each package reached, the package that imports it.
	via := map[string]string{}
	var queue []string
	start, err := os.ReadDir(filepath.Join(root, "internal/sketch/eval"))
	if err != nil {
		t.Fatal(err)
	}
	queue = append(queue, module+"internal/sketch/eval")
	via[queue[0]] = ""
	for _, e := range start {
		if e.IsDir() && e.Name() != "testdata" {
			p := module + "internal/sketch/eval/" + e.Name()
			queue, via[p] = append(queue, p), module+"internal/sketch/eval"
		}
	}
	for len(queue) > 0 {
		pkg := queue[0]
		queue = queue[1:]
		own := pkg == module+"internal/sketch/eval" || strings.HasPrefix(pkg, module+"internal/sketch/eval/")
		for _, imp := range productImports(t, filepath.Join(root, strings.TrimPrefix(pkg, module))) {
			for _, f := range forbidden {
				if f == module+"runtime/v1" && !own {
					continue // the checker reads its bodies' hidden arguments
				}
				if imp == f || strings.HasPrefix(imp, f+"/") {
					chain := []string{imp, pkg}
					for p := via[pkg]; p != ""; p = via[p] {
						chain = append(chain, p)
					}
					t.Fatalf("the evaluator reaches %s: %s", f, strings.Join(chain, " <- "))
				}
			}
			if _, seen := via[imp]; !seen && strings.HasPrefix(imp, module) {
				via[imp] = pkg
				queue = append(queue, imp)
			}
		}
	}
}

// productImports are the imports of the non-test .go files in dir, sorted.
func productImports(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), filepath.Join(dir, name), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range f.Imports {
			p, err := strconv.Unquote(s.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			seen[p] = true
		}
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}
