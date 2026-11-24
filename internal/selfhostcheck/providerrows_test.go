package selfhostcheck

import (
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketchfront"
)

func TestEmptyProviderRow(t *testing.T) {
	dir := filepath.Join(repoRoot(), "internal/selfhostcheck/testdata/agree/provider-empty-row")
	a, err := sketchfront.CheckDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Diagnostics) != 0 {
		t.Fatalf("Transport with its failure row omitted must accept an infallible provider:\n%v", a.Diagnostics)
	}
	a, err = sketchfront.CheckDir(filepath.Join(dir, "refused"))
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Diagnostics) != 1 || a.Diagnostics[0].Code != "S106" ||
		!strings.Contains(a.Diagnostics[0].Message, "differs in fallibility") {
		t.Fatalf("a nonempty instance still requires a fallible provider method:\n%v", a.Diagnostics)
	}
}
