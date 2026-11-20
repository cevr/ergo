package fix

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Scratch (RESPELL-SYNTAX lane, not committed): the --go respell over the
// tree, dry, each touched file's new text under RS_DRY_OUT.
func TestZZRSDry(t *testing.T) {
	root, out := os.Getenv("RS_DRY_ROOT"), os.Getenv("RS_DRY_OUT")
	if root == "" || out == "" {
		t.Skip("scratch")
	}
	var paths []string
	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			paths = append(paths, filepath.Join(root, e.Name()))
		}
	}
	rep, err := RunMut(Options{Paths: paths, Go: true, Write: false})
	if err != nil {
		t.Fatal(err)
	}
	var files []string
	for f := range rep.Files {
		files = append(files, f)
	}
	sort.Strings(files)
	for _, f := range files {
		rel, _ := filepath.Rel(root, f)
		dst := filepath.Join(out, rel)
		_ = os.MkdirAll(filepath.Dir(dst), 0o755)
		_ = os.WriteFile(dst, []byte(rep.Files[f]), 0o644)
		if os.Getenv("RS_WRITE") == "1" && !excluded(rel) {
			info, err := os.Stat(f)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(f, []byte(rep.Files[f]), info.Mode().Perm()); err != nil {
				t.Fatal(err)
			}
			t.Logf("wrote %s %d", rel, rep.Edits[f])
			continue
		}
		t.Logf("touch %s %d excluded=%v", rel, rep.Edits[f], excluded(rel))
	}
	for _, e := range rep.Errs {
		t.Logf("err %s", e)
	}
}

// excluded are the files that keep the old spelling on purpose: the fix's
// own tests and fixtures, migrate196, the syntax fixtures, the narration
// corpora (frozen copies), and the respell test of cmd/ergo.
func excluded(rel string) bool {
	for _, p := range []string{"internal/migrate196/", "internal/sketch/fix/", "internal/sketch/syntax/testdata/", "internal/sketch/lint/testdata/narration/", "cmd/ergo/fix_test.go"} {
		if strings.HasPrefix(rel, p) {
			return true
		}
	}
	return false
}
