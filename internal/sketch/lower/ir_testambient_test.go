package lower

import (
	"encoding/json"
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
	// No body is Go text, so the folder's opening is in the test's IR
	// body, which the patched compiler reads (ergoir.json). The law
	// runner's cases under onShell open folders of their own, so only
	// ergoTestWd's body counts.
	for _, f := range files {
		if !strings.HasSuffix(f.Path, IRFileName) {
			continue
		}
		var ir irFile
		if err := json.Unmarshal(f.Data, &ir); err != nil {
			t.Fatal(err)
		}
		for _, fn := range ir.Funcs {
			if fn.Name != goHelper("ergoTestWd") {
				continue
			}
			body, err := json.Marshal(fn.Body)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(body), `"TestFolderOpen"`) {
				t.Fatalf("the test's with of a context that needs TestAmbient does not open the case's folder:\n%s", body)
			}
			return
		}
	}
	t.Fatal("no IR file holds " + goHelper("ergoTestWd") + "'s body")
}
