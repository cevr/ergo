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
// tests). The IR has no defer, so the builder leaves such a with to the
// emitter, which opens the folder and removes it at the case's end. The
// check is the promised fact: the test's lowered body opens the folder.
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
	files, _, err := LowerIRTests(u)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		text := string(f.Data)
		at := strings.Index(text, "func "+goHelper("ergoTestWd")+"(")
		if at < 0 {
			continue
		}
		body := text[at:]
		if end := strings.Index(body[1:], "func "); end >= 0 {
			body = body[:end+1]
		}
		if !strings.Contains(body, ".TestFolderOpen()") {
			t.Fatalf("the test's with of a context that needs TestAmbient does not open the case's folder:\n%s", body)
		}
		return
	}
	t.Fatal("no lowered file declares " + goHelper("ergoTestWd"))
}
