package main

import (
	"os"
	"path/filepath"
	"testing"
)

// The coverage store's index (CS-ROUND) names a commit only for a clean
// checkout, and only the suites whose segment the store holds: a reader
// diffs that commit against its change, so an entry for a dirty tree or
// a missing segment would select from a record of another tree.
//
// A suite of named tests (r) is no record of its package and is not
// indexed.
//
// Mutants (testdata/mutants.json): csr-index-dirty-tree,
// csr-index-untracked-ignored, csr-index-unstored-suite,
// csr-index-partial-suite.
func TestIndexNamesOnlyCleanCommitsWithStoredSegments(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init", "-q")
	writeFile(t, filepath.Join(repo, "a.txt"), "one\n")
	git(t, repo, "add", "a.txt")
	git(t, repo, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "one")
	head := git(t, repo, "rev-parse", "HEAD")
	if got := cleanCommit(repo); got != head {
		t.Fatalf("clean checkout: cleanCommit = %q, want HEAD %s", got, head)
	}
	writeFile(t, filepath.Join(repo, "a.txt"), "two\n")
	if got := cleanCommit(repo); got != "" {
		t.Fatalf("a changed tracked file: cleanCommit = %q, want none", got)
	}
	writeFile(t, filepath.Join(repo, "a.txt"), "one\n")
	writeFile(t, filepath.Join(repo, "b.txt"), "new\n")
	if got := cleanCommit(repo); got != "" {
		t.Fatalf("an untracked file: cleanCommit = %q, want none", got)
	}
	if err := os.Remove(filepath.Join(repo, "b.txt")); err != nil {
		t.Fatal(err)
	}

	store := coverStore{Dir: t.TempDir()}
	stored, missing := "aa"+"11", "bb"+"22"
	if err := store.Save(stored, &coverage{Tests: []*covTest{{ID: "p:TestA"}}}); err != nil {
		t.Fatal(err)
	}
	suites := []Suite{{Name: "p", Package: "./p"}, {Name: "q", Package: "./q"}, {Name: "r", Package: "./r", Only: []string{"TestA"}}}
	n, err := indexSegments(store, head, repo, suites, map[string]string{"p": stored, "q": missing, "r": stored}, []string{"./p"})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := store.At(head)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 || len(entries) != 1 || entries[0].Suite != "p" || entries[0].Key != stored || entries[0].Package != "./p" || entries[0].Mode != "full" || len(entries[0].Cover) != 1 || entries[0].GoEnv == "" {
		t.Fatalf("indexed %d: %+v; want the one suite whose segment is stored, in full mode", n, entries)
	}
}
