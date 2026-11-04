package closure

import (
	"os"
	"path/filepath"
	"testing"
)

// The round-1 verifier's probes (round-1-receipts/classifier-probe.patch),
// kept as regression tests of M1 and M2.

// TestReviewSymlinkDotDot: a link in testdata followed by ".." reads a
// document outside the closure; the check reports it.
func TestReviewSymlinkDotDot(t *testing.T) {
	root := t.TempDir()
	own := filepath.Join(root, "internal/a/testdata")
	docs := filepath.Join(root, "docs/research")
	for _, p := range []string{own, docs} {
		if err := os.MkdirAll(p, 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "docs/tooling.md"), []byte("outside closure"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(docs, filepath.Join(own, "link")); err != nil {
		t.Fatal(err)
	}
	raw := own + "/link/../tooling.md"
	b, err := os.ReadFile(raw)
	if err != nil || string(b) != "outside closure" {
		t.Fatalf("real read: %q %v", b, err)
	}
	entries, err := parseLog([]byte("# test log\nopen "+raw+"\nstat "+own+"/link\n"), own, []string{root})
	if err != nil {
		t.Fatal(err)
	}
	c := newClosure("internal/a", nil, nil)
	outside := c.check(entries, map[string]bool{})
	t.Logf("real read docs/tooling.md; classified entries=%v outside=%v", entries, outside)
	if len(outside) == 0 {
		t.Fatal("undeclared docs/tooling.md read passed the closure check")
	}
}

// TestReviewAllowanceBreadth: the checker's declaration of the migrated
// examples does not grant a deep read below them.
func TestReviewAllowanceBreadth(t *testing.T) {
	c := newClosure("internal/sketch/check", nil, nil)
	e := entry{"open", "docs/research/ef-migrate/unused/deep/input.txt"}
	used := map[string]bool{}
	if len(c.check([]entry{e}, used)) == 0 {
		t.Fatalf("non-corpus content read accepted and marks declaration used: %v %v", e, used)
	}
}
