package sketchfront

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestGoLibraryNamesTheErgoFiles: the Go package `ergo go` builds for an
// Ergo package names each .ergo file by its absolute path, in the
// declaration file's //line directives and in the IR file's files, since
// the declaration file reaches the compile from ergo's cache through
// -overlay, where a path relative to it names a file of the cache
// (GO-DROPIN-PROBE: errors at ../libout/lib/lib.ergo); and an Ergo package
// that imports another is refused, since the lowering would copy the
// import's types into the importer's Go package. It checks in process.
//
// Mutants: sketchfront-golib-relative-lines (GoLibrary keeps the
// module-relative names), sketchfront-golib-imports-copied (the refusal
// is dropped).
func TestGoLibraryNamesTheErgoFiles(t *testing.T) {
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
	write("ergo.mod", "module example.com/mix\n")
	write("lib/lib.ergo", "// Add is a + b.\nfunc Add(a int, b int) int { a + b }\n\n// Sum is the sum of xs.\nfunc Sum(xs []int) int {\n\tlet mut s = 0\n\tfor x in xs {\n\t\ts += x\n\t}\n\ts\n}\n")
	write("top/top.ergo", "import example.com/mix/lib\n\n// Twice is 2n.\nfunc Twice(n int) int { lib.Add(n, n) }\n")
	a, err := CheckDir(filepath.Join(root, "lib"))
	if err != nil || len(a.Diagnostics) > 0 {
		t.Fatalf("lib does not check: %v %v", err, a.Diagnostics)
	}
	files, err := GoLibrary(a, "lib")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "lib", "lib.ergo")
	directive := regexp.MustCompile(`(?://line |/\*line )([^:*]+):\d+`)
	lines, irs := 0, 0
	for _, f := range files {
		switch f.Path {
		case "lib.go":
			for _, m := range directive.FindAllStringSubmatch(string(f.Data), -1) {
				lines++
				if m[1] != want {
					t.Fatalf("lib.go: %s names %s, want %s", strings.TrimSpace(m[0]), m[1], want)
				}
			}
		case "ergoir.json":
			var ir struct{ Files []string }
			if err := json.Unmarshal(f.Data, &ir); err != nil {
				t.Fatal(err)
			}
			for _, name := range ir.Files {
				irs++
				if name != want {
					t.Fatalf("ergoir.json names %s, want %s", name, want)
				}
			}
		}
	}
	if lines == 0 || irs == 0 {
		t.Fatalf("lib.go has %d line directives and ergoir.json %d files: want both", lines, irs)
	}
	top, err := CheckDir(filepath.Join(root, "top"))
	if err != nil || len(top.Diagnostics) > 0 {
		t.Fatalf("top does not check: %v %v", err, top.Diagnostics)
	}
	if _, err := GoLibrary(top, "top"); err == nil || !strings.Contains(err.Error(), "imports the Ergo package example.com/mix/lib") {
		t.Fatalf("GoLibrary(top) = %v, want the refusal of its import of example.com/mix/lib", err)
	}
}
