package fix

import (
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
)

// keepOldSpellings are the files of the repository whose old spellings are
// the point: the inputs of the respell's own tests and of the tests that
// the old spellings still parse or are refused. The respell would rewrite
// each of them, so TestTreeHasNoOldSpelling leaves them, by name, and fails
// when one no longer holds an old spelling (a stale name).
var keepOldSpellings = []string{
	"cmd/ergo/fix_test.go",                       // TestFixRespellsAStdThatDoesNotCheck's std input
	"internal/sketch/fix/mut_test.go",            // the mut respell's inputs
	"internal/sketch/fix/mutfrag_test.go",        // the fragment respell's inputs
	"internal/sketch/fix/testdata/mutgo/lits.go", // TestMutGoLeavesGoSource's input
}

// TestTreeHasNoOldSpelling is ergo fix --dry-run --go over the repository,
// its syntactic respells only (RunMut): it finds nothing to
// rewrite and nothing it would leave, outside keepOldSpellings. Generators
// and tests that write an old spelling again turn it red here, where only
// their own run would have (I22 at RESPELL-SYNTAX's merged head: 7 own and
// 50 mut rewrites).
func TestTreeHasNoOldSpelling(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	opts := Options{Paths: []string{root}, Go: true}
	muts, err := RunMut(opts)
	if err != nil {
		t.Fatal(err)
	}
	rel := func(file string) string {
		r, err := filepath.Rel(root, file)
		if err != nil {
			return file
		}
		return filepath.ToSlash(r)
	}
	found := map[string]bool{}
	var bad []string
	note := func(file, what string) {
		r := rel(file)
		if slices.Contains(keepOldSpellings, r) {
			found[r] = true
			return
		}
		bad = append(bad, r+": "+what)
	}
	for file := range muts.Edits {
		note(file, "mut respell")
	}
	for _, e := range muts.Errs {
		bad = append(bad, "not rewritten: "+e)
	}
	for _, k := range muts.Kept {
		file := k[:strings.LastIndexByte(k, ':')]
		note(file, "left at "+rel(k))
	}
	sort.Strings(bad)
	for _, b := range bad {
		t.Errorf("%s (respell with ergo fix --go, or name the file in keepOldSpellings)", b)
	}
	for _, k := range keepOldSpellings {
		if !found[k] {
			t.Errorf("%s holds no old spelling: delete it from keepOldSpellings", k)
		}
	}
}
