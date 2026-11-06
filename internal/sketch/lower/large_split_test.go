package lower

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"
)

// largeTests are the tests that ran too long to balance in a shard
// (check-split-2, batch 9: TestTestRunnerModel 16.1 s, 25.6 s under load).
var largeTests = []string{"TestTestRunnerModel"}

// TestLargeTestsAreSplit: each large test is a registered split of two
// parts or more, and no test function of its own name is left, so its
// domain runs only through the parts (TestSplitsCoverTheirDomain checks
// that they cover it).
func TestLargeTestsAreSplit(t *testing.T) {
	funcs := testFuncNames(t)
	for _, name := range largeTests {
		parts := 0
		for _, s := range splits {
			if s.splitName() == name {
				parts = s.splitParts()
			}
		}
		if parts < 2 {
			t.Errorf("%s is not a split of two parts or more (parts %d)", name, parts)
		}
		if funcs[name] {
			t.Errorf("%s is still a test function: its domain runs in one test", name)
		}
	}
}

// testFuncNames are the top-level functions of the package's test files.
func testFuncNames(t *testing.T) map[string]bool {
	t.Helper()
	files, err := filepath.Glob("*_test.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("no test files: %v", err)
	}
	out := map[string]bool{}
	fset := token.NewFileSet()
	for _, f := range files {
		file, err := parser.ParseFile(fset, f, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range file.Decls {
			if fn, ok := d.(*ast.FuncDecl); ok && fn.Recv == nil {
				out[fn.Name.Name] = true
			}
		}
	}
	return out
}
