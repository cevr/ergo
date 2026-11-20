package sketchfront

import (
	"os"
	"path/filepath"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

// TestLawKeyFollowsTheGoBody (LAWCACHE-GOKEY): the store's key of a law's
// verdict (lawcache) takes the foreign key of the Go code the law reaches,
// as the ledger's digest does, so an edit of a module Go body with no
// .ergo file edited is not answered by the verdict of the old body. Found
// in U1 SIMD-SCAN, where a kernel's laws printed "(cached)" from the
// verdicts of another body. Mutant: law-key-no-foreign.
func TestLawKeyFollowsTheGoBody(t *testing.T) {
	root := writeTree(t, map[string]string{
		"go.mod":         "module example.com/app\n\ngo 1.27\n",
		"ergo.mod":       "module app\ntrust go app\n",
		"kern/kern.go":   "package kern\n\n//ergo:wait none\nfunc Index(s string, c uint8) int { return -1 }\n",
		"scan/scan.ergo": "extern go example.com/app/kern.Index func index(s string, c byte) int\n\nlaw IndexIsSmall(s string, c byte) {\n\tindex(s, c) <= 0\n}\n",
	})
	key := func() string {
		a, err := CheckDir(filepath.Join(root, "scan"))
		if err != nil {
			t.Fatal(err)
		}
		pkg := a.rootPackage()
		if pkg == nil {
			t.Fatalf("no checked package:\n%s", diagnosticLines(a))
		}
		targets := lower.RunTargets(pkg)
		if len(targets) != 1 {
			t.Fatalf("%d law runs, want 1", len(targets))
		}
		return targetKey(pkg, targets[0], 1, 0, runnerNative, foreignKeys(a, nil))
	}
	before := key()
	edited := "package kern\n\n//ergo:wait none\nfunc Index(s string, c uint8) int { return 0 }\n"
	if err := os.WriteFile(filepath.Join(root, "kern", "kern.go"), []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	if key() == before {
		t.Errorf("law IndexIsSmall keeps its verdict key after its Go body changed; want a new key")
	}
}
