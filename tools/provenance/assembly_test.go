package provenance

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// assemblyChild names the assembly a child run of
// TestAssemblyRefusesASecondWrite performs; the parent expects it to fail.
const assemblyChild = "PROVENANCE_ASSEMBLY_CHILD"

// TestAssemblyRefusesASecondWrite is the regression for E2d round 2 review
// M1: TestControls copied testdata/controls and then testdata/accepts into
// one directory, and a file name both shared let the accepts file replace
// the control, its source and its // want lines, before analysistest saw
// them. Each case assembles, in a child run of this test, files that share
// a destination, and expects the run to fail naming both origins; the last
// case patches a file the package does not have, which would drop the
// patch's controls the same way. The assembly helpers call t.Fatal, so the
// child is a separate process.
func TestAssemblyRefusesASecondWrite(t *testing.T) {
	if name := os.Getenv(assemblyChild); name != "" {
		assembleColliding(t, name)
		return
	}
	for _, c := range []struct {
		name  string
		wants []string
	}{
		// Two fixture sets that share a file name (controls, then accepts).
		{"fixture-sets", []string{"twice", filepath.Join("controls", "ctl_shared.go"), filepath.Join("accepts", "ctl_shared.go")}},
		// A fixture named like a copied checker file.
		{"checker-file", []string{"twice", filepath.Join("check", "stmt.go"), filepath.Join("controls", "stmt.go")}},
		// A patch whose file the package no longer has: its controls
		// would never be written.
		{"stale-patch", []string{"gone.go", "does not have"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestAssemblyRefusesASecondWrite$", "-test.count=1")
			cmd.Env = append(os.Environ(), assemblyChild+"="+c.name)
			out, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) {
				t.Fatalf("the assembly passed (err %v): a file or a patch was dropped unseen\n%s", err, out)
			}
			for _, want := range c.wants {
				if !strings.Contains(string(out), want) {
					t.Fatalf("the failed assembly does not name %q\n%s", want, out)
				}
			}
		})
	}
}

// assembleColliding runs one case's assembly in the child.
func assembleColliding(t *testing.T, name string) {
	root := t.TempDir()
	put := func(rel, data string) {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	module := filepath.Join(root, "module")
	switch name {
	case "fixture-sets":
		put(filepath.Join("controls", "ctl_shared.go"), "package check\n\nvar control int\n")
		put(filepath.Join("accepts", "ctl_shared.go"), "package check\n")
		copyDir(t, filepath.Join(root, "controls"), module)
		copyDir(t, filepath.Join(root, "accepts"), module)
	case "checker-file":
		put(filepath.Join("check", "stmt.go"), "package check\n")
		put(filepath.Join("controls", "stmt.go"), "package check\n\nvar control int\n")
		copyPackage(t, listed{Dir: filepath.Join(root, "check"), GoFiles: []string{"stmt.go"}}, module, nil)
		copyDir(t, filepath.Join(root, "controls"), module)
	case "stale-patch":
		put(filepath.Join("check", "stmt.go"), "package check\n")
		edits := map[string][]struct{ old, new string }{"gone.go": {{"package check\n", "package check\n\nvar control int\n"}}}
		copyPackage(t, listed{Dir: filepath.Join(root, "check"), GoFiles: []string{"stmt.go"}}, module, edits)
	default:
		t.Fatalf("unknown assembly case %q", name)
	}
}
