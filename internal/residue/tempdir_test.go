package residue_test

import (
	"bufio"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// tempDirAllowlist lists the test functions that make a directory or file
// in the system temporary directory and leave its removal to another
// function (a helper returning it to a caller that removes it), one
// `path<TAB>function` per line.
const tempDirAllowlist = "testdata/tempdir-allow.txt"

// TestTestsRemoveTheirTemporaryFiles: a test, TestMain, init or helper
// that makes a directory or a file in the system temporary directory
// (os.MkdirTemp or os.CreateTemp with "" or os.TempDir() as the place)
// removes it in the same function: os.Remove or os.RemoveAll of the
// variable or field that holds it, or a Cleanup (t.TempDir needs none). cmd/ergo's init made /tmp/ergo-cmd-test-cache-*
// in every test process and never removed it: 7,404 directories, 68 GB, on
// 2026-11-13 (GS18). It reads every _test.go of the module but testdata;
// a function the allowlist names may leave the removal to its caller, and
// an allowlist row that names no such function fails, so the list only
// shrinks.
func TestTestsRemoveTheirTemporaryFiles(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	allowed := readTempAllowlist(t)
	used := map[string]bool{}
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && (strings.HasPrefix(d.Name(), ".") || d.Name() == "testdata" || d.Name() == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		for _, leak := range tempLeaks(t, rel, src) {
			key := rel + "\t" + leak.function
			if allowed[key] {
				used[key] = true
				continue
			}
			t.Errorf("%s:%d: %s makes a temporary file or directory in the system temporary directory and does not remove it; use t.TempDir(), or os.RemoveAll or t.Cleanup in %s", rel, leak.line, leak.function, leak.function)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for key := range allowed {
		if !used[key] {
			t.Errorf("%s: the row %q names no function that leaves a temporary file to its caller; remove it", tempDirAllowlist, key)
		}
	}
}

// tempLeak is a function that makes a temporary file it does not remove.
type tempLeak struct {
	function string
	line     int
}

// tempLeaks returns the top-level functions of a Go source that call
// os.MkdirTemp or os.CreateTemp in the system temporary directory and do
// not remove what they made in their body (a deferred closure counts): an
// os.Remove or os.RemoveAll whose argument names the variable or field the
// call's result went to, or any Cleanup call. A result assigned to nothing
// is removed only by a Cleanup.
func tempLeaks(t *testing.T, name string, src []byte) []tempLeak {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, name, src, 0)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var leaks []tempLeak
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		// First the calls whose result an assignment names.
		assigned := map[*ast.CallExpr]string{}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if x, ok := n.(*ast.AssignStmt); ok && len(x.Rhs) == 1 && len(x.Lhs) > 0 {
				if call, ok := x.Rhs[0].(*ast.CallExpr); ok {
					assigned[call] = assignedName(x.Lhs[0])
				}
			}
			return true
		})
		type making struct {
			name string // "" when no variable or field holds the result
			pos  token.Pos
		}
		var made []making
		removed := map[string]bool{} // the names a removal's argument names
		cleanup := false
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch {
			case makesSystemTemp(call):
				made = append(made, making{assigned[call], call.Pos()})
			case isPkgCall(call, "os", "Remove"), isPkgCall(call, "os", "RemoveAll"):
				for _, arg := range call.Args {
					ast.Inspect(arg, func(n ast.Node) bool {
						if id, ok := n.(*ast.Ident); ok {
							removed[id.Name] = true
						}
						return true
					})
				}
			default:
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Cleanup" {
					cleanup = true
				}
			}
			return true
		})
		for _, m := range made {
			if cleanup || (m.name != "" && m.name != "_" && removed[m.name]) {
				continue
			}
			name := fn.Name.Name
			if fn.Recv != nil && len(fn.Recv.List) > 0 {
				name = tempRecvName(fn.Recv.List[0].Type) + "." + name
			}
			leaks = append(leaks, tempLeak{name, fset.Position(m.pos).Line})
			break
		}
	}
	return leaks
}

// makesSystemTemp reports whether call is os.MkdirTemp or os.CreateTemp in
// the system temporary directory.
func makesSystemTemp(call *ast.CallExpr) bool {
	return (isPkgCall(call, "os", "MkdirTemp") || isPkgCall(call, "os", "CreateTemp")) && len(call.Args) > 0 && inSystemTemp(call.Args[0])
}

// assignedName is the variable, or the field, an assignment's first
// left-hand side names.
func assignedName(lhs ast.Expr) string {
	switch x := lhs.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		return x.Sel.Name
	}
	return ""
}

func isPkgCall(call *ast.CallExpr, pkg, name string) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != name {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == pkg
}

// inSystemTemp reports whether a MkdirTemp or CreateTemp place is the
// system temporary directory: "" or os.TempDir().
func inSystemTemp(dir ast.Expr) bool {
	if lit, ok := dir.(*ast.BasicLit); ok && lit.Kind == token.STRING {
		s, err := strconv.Unquote(lit.Value)
		return err == nil && s == ""
	}
	call, ok := dir.(*ast.CallExpr)
	return ok && isPkgCall(call, "os", "TempDir")
}

func tempRecvName(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.StarExpr:
		return tempRecvName(x.X)
	case *ast.Ident:
		return x.Name
	case *ast.IndexExpr:
		return tempRecvName(x.X)
	}
	return "?"
}

func readTempAllowlist(t *testing.T) map[string]bool {
	t.Helper()
	f, err := os.Open(tempDirAllowlist)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	allowed := map[string]bool{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !strings.Contains(line, "\t") {
			t.Fatalf("%s: row %q is not path<TAB>function", tempDirAllowlist, line)
		}
		allowed[line] = true
	}
	return allowed
}

// TestTempLeakRule pins the reading: a leak, a removal by defer, by a
// Cleanup and by a file's name, a place that is not the system temporary
// directory, a function that removes nothing it made, and one that
// removes another directory than the one it made. It catches
// tempdir-remove-ignored, tempdir-place-ignored and tempdir-any-removal (this package's ledger), which the checkout
// alone does not once every leak is fixed.
func TestTempLeakRule(t *testing.T) {
	src := `package p

func init() { dir, _ := os.MkdirTemp("", "x-"); _ = dir }

func a() { dir, _ := os.MkdirTemp(os.TempDir(), "x-"); defer func() { os.RemoveAll(dir) }() }

func b(t *testing.T) { f, _ := os.CreateTemp("", "x-"); t.Cleanup(func() {}); _ = f }

func c(d string) { os.MkdirTemp(d, "x-") }

func (s *S) d() { os.CreateTemp("", "x-") }

func e() { s.dir, _ = os.MkdirTemp("", "x-"); b, _ := os.MkdirTemp("", "y-"); defer os.RemoveAll(b) }

func f() { g, _ := os.CreateTemp("", "x-"); os.Remove(g.Name()) }
`
	var got []string
	for _, l := range tempLeaks(t, "p_test.go", []byte(src)) {
		got = append(got, l.function+":"+strconv.Itoa(l.line))
	}
	if strings.Join(got, " ") != "init:3 S.d:11 e:13" {
		t.Fatalf("leaks %v; want init:3 S.d:11 e:13", got)
	}
}
