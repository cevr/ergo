package residue_test

import (
	"bufio"
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// vendoredList names each package an example carries as a copy of an
// installable module's package, until modules can require each other
// (MD1): `copy<TAB>module package`, both relative to the repository.
const vendoredList = "testdata/vendored.txt"

// TestVendoredCopiesMatch: an example that installs a module by copying
// its package (examples/catalog's xml is modules/xml/xml) holds the same
// .ergo files, byte for byte, so the example's tests and cases run the
// module as it is, and an edit to either side alone fails here. Mutant:
// vendored-copy-drifts (testdata/mutants.json).
func TestVendoredCopiesMatch(t *testing.T) {
	root, err := exec.Command("git", "--no-optional-locks", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("find the repository: %v", err)
	}
	top := strings.TrimSpace(string(root))
	f, err := os.Open(vendoredList)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rows := 0
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := s.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		copyDir, module, ok := strings.Cut(line, "\t")
		if !ok {
			t.Fatalf("%s: %q is not copy<TAB>module", vendoredList, line)
		}
		rows++
		want, got := ergoFiles(t, filepath.Join(top, module)), ergoFiles(t, filepath.Join(top, copyDir))
		if !slices.Equal(names(want), names(got)) {
			t.Errorf("%s holds %v, the module %s holds %v", copyDir, names(got), module, names(want))
			continue
		}
		for name, data := range want {
			if !bytes.Equal(data, got[name]) {
				t.Errorf("%s/%s differs from %s/%s: copy the module's file again", copyDir, name, module, name)
			}
		}
	}
	if err := s.Err(); err != nil {
		t.Fatal(err)
	}
	if rows == 0 {
		t.Fatalf("%s names no copy", vendoredList)
	}
}

// ergoFiles is each .ergo file of dir, by name.
func ergoFiles(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".ergo") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		files[e.Name()] = data
	}
	return files
}

func names(files map[string][]byte) []string {
	out := make([]string, 0, len(files))
	for name := range files {
		out = append(out, name)
	}
	slices.Sort(out)
	return out
}
