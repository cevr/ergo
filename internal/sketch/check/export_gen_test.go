package check

import (
	"flag"
	"os"
	"path/filepath"
	"testing"

	"ergo.local/prototype/internal/sketch/exportgen"
)

var updateExport = flag.Bool("update-export", false, "rewrite export_gen.go and syntax's codec_gen.go from internal/sketch/exportgen")

// TestExportCodecsAreCurrent is the staleness check of the generated
// export codecs (CHK1b): export_gen.go and internal/sketch/syntax's
// codec_gen.go must be what exportgen writes from the two packages today,
// so a field added to the checker's model or to a syntax node is exported
// or the build of this test fails, never dropped silently. Rewrite them
// with: go test ./internal/sketch/check -run TestExportCodecsAreCurrent -update-export
func TestExportCodecsAreCurrent(t *testing.T) {
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	syntaxSrc, checkSrc, err := exportgen.Generate(root)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{
		filepath.Join(root, "internal/sketch/syntax/codec_gen.go"): syntaxSrc,
		filepath.Join(root, "internal/sketch/check/export_gen.go"): checkSrc,
	}
	for path, want := range files {
		if *updateExport {
			if err := os.WriteFile(path, want, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(want) {
			t.Errorf("%s is not what exportgen writes; rewrite it with go test ./internal/sketch/check -run TestExportCodecsAreCurrent -update-export", path)
		}
	}
}
