package residue_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// stdCopies are the local helpers an example kept from before std held
// them (STD-SURVEY §6, unit ST0): a declaration's name and the shape of
// its body, and the std function that does its work.
var stdCopies = []struct{ name, shape, std string }{
	{"without", `for [^{]*\{\s*if [^{]*!= [^{]*\{\s*\w+ = \w+\.Append\(\w+\)\s*\}\s*\}\s*\w+\s*$`, "slices.Filter, or slices.Delete for an index"},
	{"indexOf", `for \w+, \w+ in [^{]*\{\s*if [^{]*== [^{]*\{\s*return \w+\s*\}\s*\}\s*-1\s*$`, "slices.Index or slices.IndexFunc"},
	{"sortedKeys", `for \w+, _ in \w+ \{\s*\w+ = \w+\.Append\(\w+\)\s*\}\s*slices\.Sort\(\w+\)\s*$`, "slices.Sorted(maps.Keys(m))"},
}

// TestExamplesKeepNoStdCopy: no example declares a helper that is a copy
// of a std function (stdCopies): a function of a row's name whose body has
// the row's shape. A helper with that name and another body is the
// example's own and is not read.
func TestExamplesKeepNoStdCopy(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	decl := regexp.MustCompile(`(?m)^func (?:\(\w+ [^)]*\) )?(\w+)(?:\[[^\]]*\])?\([^\n]*\{\n((?:.*\n)*?)\}\n`)
	err = filepath.WalkDir(filepath.Join(root, "examples"), func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".ergo") {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range decl.FindAllStringSubmatch(string(data), -1) {
			for _, c := range stdCopies {
				if m[1] == c.name && regexp.MustCompile(c.shape).MatchString(m[2]) {
					rel, _ := filepath.Rel(root, path)
					t.Errorf("%s: func %s copies std; use %s", filepath.ToSlash(rel), c.name, c.std)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
