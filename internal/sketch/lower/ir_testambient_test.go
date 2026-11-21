package lower

import (
	"strings"
	"testing"
)

// TestIRWithOfTestAmbientOpensTheFolder is the smallest repro of the
// SPLIT F2 bug: on the IR path, a test's `with` of a context that needs
// TestAmbient copied the environment and built the providers, but never
// opened the case's folder nor put the runner's TestAmbient in the copy,
// so exec.NewTestHost read a nil TestAmbient and the test panicked (nil
// pointer dereference in prelude TestFolder; modules/work git and reflink
// tests). The IR now opens the folder itself and defers the provider's
// end, which removes it at the case's end (G10). The check is the
// promised fact: the test's lowered body opens the folder, and no body
// falls back to the emitter.
// Mutant (testdata/mutants.json): ir-with-test-ambient-not-opened.
func TestIRWithOfTestAmbientOpensTheFolder(t *testing.T) {
	const src = `import os

let onShell = context needs TestAmbient {
	os.Process: os.NewTestHost(),
}

test Wd {
	with onShell {
		let w = try os.Process.Getwd()
		assert w != ""
	}
}
`
	u := checkPackage(t, "main", []pkgSource{{testSourcePath, src}}, nil)
	files, report, err := LowerIRTests(u)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Fallback) > 0 {
		t.Fatalf("bodies fall back to the emitter: %v", report.Fallback)
	}
	// No body is Go text, so the folder's opening is in the IR the
	// patched compiler reads (ergoir.json), the one place it can be.
	var names []string
	for _, f := range files {
		names = append(names, f.Path)
		if strings.Contains(string(f.Data), "TestFolderOpen") {
			return
		}
	}
	t.Fatalf("the test's with of a context that needs TestAmbient does not open the case's folder: no lowered file (%v) names TestFolderOpen", names)
}
