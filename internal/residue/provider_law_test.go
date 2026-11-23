package residue_test

import (
	"os"
	"path/filepath"
	"testing"
)

// TestErgoKillerResolvesProviderLaws: a ledger's killer DIR:law:pkg.T.L
// names a member law of the operation type T that DIR's package imports as
// pkg, which DIR's providers run under that qualified name
// (LAW-RUNS-CREDIT-PROVIDERS): it resolves to the law the imported package
// declares, and a law no imported package declares, or a qualifier DIR
// does not import, stays missing. Mutant: lawkiller-provider-law-unresolved.
func TestErgoKillerResolvesProviderLaws(t *testing.T) {
	root := t.TempDir()
	for f, text := range map[string]string{
		"internal/sketch/std/w/w.ergo":        "type Store {\n\tGet() int,\n\tlaw Kept(n int) { true },\n}\n",
		"internal/sketch/std/w/memory/m.ergo": "import w\n\nfunc get() int { 1 }\n",
		"app/ergo.mod":                        "module app\n",
		"app/store/s.ergo":                    "type Box {\n\tPut(n int),\n\tlaw Holds(n int) { true },\n}\n",
		"app/disk/d.ergo":                     "import app/store\n\nfunc put() {}\n",
	} {
		p := filepath.Join(root, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	files := func(dir string) []string {
		out, _ := filepath.Glob(filepath.Join(root, filepath.FromSlash(dir), "*.ergo"))
		return out
	}
	for _, c := range []struct {
		dir, test string
		found     bool
	}{
		{"internal/sketch/std/w/memory", "law:w.Store.Kept", true},
		{"app/disk", "law:store.Box.Holds", true},
		{"internal/sketch/std/w/memory", "law:w.Store.Gone", false},
		{"internal/sketch/std/w/memory", "law:v.Store.Kept", false},
		{"internal/sketch/std/w/memory", "law:Store.Kept", false},
	} {
		missing := ergoKillerMissing(root, c.dir, c.test, files(c.dir))
		if (missing == "") != c.found {
			t.Errorf("%s:%s: missing %q; want found %v", c.dir, c.test, missing, c.found)
		}
	}
}
