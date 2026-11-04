package lower

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// closedStage matches a string that names check-v1 stage E, which is
// closed: a refusal names its owner, never a closed stage (C43).
var closedStage = regexp.MustCompile(`check-v1 stage E|stage E's|after stage E\b`)

// TestNoStageERefusal is C43's guard (check-v1 E6b): no string literal in
// the compiler's code (internal/sketch, internal/sketchfront, cmd/ergo; not
// tests) names check-v1 stage E. Refusal and error texts are string
// literals (refuse, ice, errorf and their callers), so a refusal that
// waits for a closed stage cannot come back. Comments may cite a finished
// sub-stage as a rule's origin; they are not literals.
func TestNoStageERefusal(t *testing.T) {
	fset := token.NewFileSet()
	files := 0
	for _, root := range []string{"..", "../../sketchfront", "../../../cmd/ergo"} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "testdata" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			f, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				return err
			}
			files++
			ast.Inspect(f, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				text, err := strconv.Unquote(lit.Value)
				if err != nil {
					text = lit.Value
				}
				if closedStage.MatchString(text) {
					t.Errorf("%s: a string names the closed check-v1 stage E: %s", fset.Position(lit.Pos()), lit.Value)
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if files < 100 {
		t.Fatalf("scanned %d Go files", files)
	}
}
