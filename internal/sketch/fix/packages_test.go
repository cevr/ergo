package fix

import (
	"os"
	"path/filepath"
	"testing"
)

const libSource = "type LE {}\n\nfunc Get() int fails LE {\n\t0\n}\n"

func midSource(written bool) string {
	clause := ""
	if written {
		clause = " fails lib.LE"
	}
	return "import m/lib\n\nfunc Mid() int" + clause + " {\n\tlet v = try lib.Get()\n\tv\n}\n"
}

// packages writes g as the root package of module m beside packages mid
// and lib: mid.Mid fails lib.LE, which the root package does not import
// until a clause names it. It returns the root file's key, the texts
// with the clauses deleted and written, mid's key and the Checker.
func packages(t *testing.T, g *program, src string) (string, map[string]string, map[string]string, string, Checker) {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"ergo.mod":     "module m\n",
		"lib/lib.ergo": libSource,
		"mid/mid.ergo": midSource(g.midWritten),
		"main.ergo":    src,
	}
	for name, text := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	key, mid, lib := filepath.Join(dir, "main.ergo"), filepath.Join(dir, "mid", "mid.ergo"), filepath.Join(dir, "lib", "lib.ergo")
	texts := map[string]string{key: src, mid: midSource(g.midWritten), lib: libSource}
	full := map[string]string{key: g.full(), mid: midSource(true), lib: libSource}
	return key, texts, full, mid, Dir(dir)
}
