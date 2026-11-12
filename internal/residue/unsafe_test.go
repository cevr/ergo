package residue_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// TestUnsafeBodiesListed holds the inventory of runtime/v1's unsafe bodies
// (unsafe.txt; owner q112): every function of a non-test runtime/v1 file
// that names package unsafe is listed, every listed function still names
// it, and every line states its invariant and its review. A use outside a
// function is named by its file and "-".
//
// Mutants it alone catches (testdata/mutants.md): an unsafe body added
// unlisted; a listing left after its body stops naming unsafe; a listing
// with no review.
func TestUnsafeBodiesListed(t *testing.T) {
	root, err := exec.Command("git", "--no-optional-locks", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("git rev-parse: %v", err)
	}
	repo := strings.TrimSpace(string(root))
	ls := exec.Command("git", "--no-optional-locks", "ls-files", "-z", "--", "runtime/v1/*.go")
	ls.Dir = repo
	list, err := ls.Output()
	if err != nil {
		t.Fatalf("git ls-files: %v", err)
	}
	var files []string
	for _, name := range strings.Split(strings.TrimSuffix(string(list), "\x00"), "\x00") {
		if name != "" && !strings.HasSuffix(name, "_test.go") && !strings.Contains(strings.TrimPrefix(name, "runtime/v1/"), "/") {
			files = append(files, name)
		}
	}
	// Files added in the working tree and not yet tracked count too.
	if extra, err := filepath.Glob(filepath.Join(repo, "runtime", "v1", "*.go")); err == nil {
		for _, p := range extra {
			name := "runtime/v1/" + filepath.Base(p)
			if !strings.HasSuffix(name, "_test.go") && !slices.Contains(files, name) {
				files = append(files, name)
			}
		}
	}
	found := map[string]bool{}
	for _, name := range files {
		src, err := os.ReadFile(filepath.Join(repo, name))
		if err != nil {
			t.Fatal(err)
		}
		for _, fn := range unsafeUses(t, name, src) {
			found[name+" "+fn] = true
		}
	}
	data, err := os.ReadFile("unsafe.txt")
	if err != nil {
		t.Fatal(err)
	}
	listed := map[string]bool{}
	for n, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Split(line, " | ")
		if len(parts) != 3 || !strings.HasPrefix(parts[1], "invariant: ") || len(strings.TrimPrefix(parts[1], "invariant: ")) == 0 ||
			!strings.HasPrefix(parts[2], "review: ") || !strings.Contains(parts[2], ", 20") {
			t.Errorf("unsafe.txt:%d: want \"<file> <function> | invariant: … | review: <who>, <date>\": %q", n+1, line)
			continue
		}
		listed[parts[0]] = true
		if !found[parts[0]] {
			t.Errorf("unsafe.txt:%d: %s names no use of unsafe (a stale line)", n+1, parts[0])
		}
	}
	for use := range found {
		if !listed[use] {
			t.Errorf("%s uses package unsafe and is not in internal/residue/unsafe.txt (owner q112: a reviewed body, its invariant and its review)", use)
		}
	}
}

// unsafeUses names each function of the Go file src (its method as
// Recv.Name) that selects a member of package unsafe, and "-" for a use
// outside any function.
func unsafeUses(t *testing.T, name string, src []byte) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), name, src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	local := ""
	for _, im := range f.Imports {
		if p, _ := strconv.Unquote(im.Path.Value); p == "unsafe" {
			local = "unsafe"
			if im.Name != nil {
				local = im.Name.Name
			}
		}
	}
	if local == "" {
		return nil
	}
	uses := func(n ast.Node) bool {
		hit := false
		ast.Inspect(n, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok {
				if id, ok := sel.X.(*ast.Ident); ok && id.Name == local {
					hit = true
				}
			}
			return !hit
		})
		return hit
	}
	var out []string
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok {
			if uses(d) {
				out = append(out, "-")
			}
			continue
		}
		if !uses(fd) {
			continue
		}
		fn := fd.Name.Name
		if fd.Recv != nil && len(fd.Recv.List) > 0 {
			typ := fd.Recv.List[0].Type
			if s, ok := typ.(*ast.StarExpr); ok {
				typ = s.X
			}
			if ix, ok := typ.(*ast.IndexExpr); ok {
				typ = ix.X
			}
			if id, ok := typ.(*ast.Ident); ok {
				fn = id.Name + "." + fn
			}
		}
		out = append(out, fn)
	}
	return out
}
