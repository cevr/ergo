package sketchfront

import (
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestGoPackagesNameTheErgoFiles: the Go packages `ergo go` builds for the
// Ergo packages of a Go module name each .ergo file by its absolute path,
// in each Go file's //line directives and in each IR file's files, since
// the Go files reach the compile from ergo's cache through -overlay, where
// a path relative to one names a file of the cache (GO-DROPIN-PROBE:
// errors at ../libout/lib/lib.ergo). The package top, which imports lib,
// is its own Go package. An ergo.mod at the module root that names
// another module than go.mod is refused. It checks in process.
//
// Mutants: sketchfront-golib-relative-lines (GoPackages keeps the
// module-relative names), sketchfront-gomodule-mismatch (an ergo.mod
// naming another module is read as the module's).
func TestGoPackagesNameTheErgoFiles(t *testing.T) {
	root := t.TempDir()
	write := func(rel, text string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.com/mix\n\ngo 1.27\n")
	write("lib/lib.ergo", "// Add is a + b.\nfunc Add(a int, b int) int { a + b }\n\n// Sum is the sum of xs.\nfunc Sum(xs []int) int {\n\tlet mut s = 0\n\tfor x in xs {\n\t\ts += x\n\t}\n\ts\n}\n")
	write("top/top.ergo", "import example.com/mix/lib\n\n// Twice is 2n.\nfunc Twice(n int) int { lib.Add(n, n) }\n")
	dirs := []string{filepath.Join(root, "lib"), filepath.Join(root, "top")}
	a, err := CheckGoModule(root, "example.com/mix", dirs)
	if err != nil || len(a.Diagnostics) > 0 {
		t.Fatalf("the module does not check: %v %v", err, a.Diagnostics)
	}
	files, err := GoPackages(a, map[string]string{"example.com/mix/lib": "lib", "example.com/mix/top": "top"})
	if err != nil {
		t.Fatal(err)
	}
	ergoFile := map[string]string{"lib": filepath.Join(root, "lib", "lib.ergo"), "top": filepath.Join(root, "top", "top.ergo")}
	directive := regexp.MustCompile(`(?://line |/\*line )([^:*]+):\d+`)
	lines, irs := map[string]int{}, map[string]int{}
	for _, f := range files {
		dir := path.Dir(f.Path)
		switch {
		case f.Path == dir+"/"+dir+".go" && ergoFile[dir] != "":
			for _, m := range directive.FindAllStringSubmatch(string(f.Data), -1) {
				if m[1] == "ergoforward.go" {
					continue // a forwarder's own name (lower.forwardFile)
				}
				lines[dir]++
				if m[1] != ergoFile[dir] {
					t.Fatalf("%s: %s names %s, want %s", f.Path, strings.TrimSpace(m[0]), m[1], ergoFile[dir])
				}
			}
		case path.Base(f.Path) == "ergoir.json" && ergoFile[dir] != "":
			var ir struct{ Files []string }
			if err := json.Unmarshal(f.Data, &ir); err != nil {
				t.Fatal(err)
			}
			for _, name := range ir.Files {
				if !filepath.IsAbs(name) {
					t.Fatalf("%s names %s, want an absolute path", f.Path, name)
				}
				irs[dir]++
			}
		}
	}
	for dir := range ergoFile {
		if lines[dir] == 0 || irs[dir] == 0 {
			t.Fatalf("%s/%s.go has %d line directives and %s/ergoir.json %d files: want both", dir, dir, lines[dir], dir, irs[dir])
		}
	}
	write("ergo.mod", "module example.com/other\n")
	if _, err := CheckGoModule(root, "example.com/mix", dirs); err == nil || !strings.Contains(err.Error(), "names the module example.com/other and go.mod example.com/mix") {
		t.Fatalf("CheckGoModule with an ergo.mod of another module = %v, want its refusal", err)
	}
}
