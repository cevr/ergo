package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/syntax"
)

// TestWriteKeepsRowComments is 196n round 2 (M1) at the command's write
// mode: `migrate196 -w` rewrites old capability rows in place, exits 0
// with no note, and the file keeps every comment of the row and parses.
func TestWriteKeepsRowComments(t *testing.T) {
	dir := t.TempDir()
	cases := map[string]string{
		"comma.ergo":    "package p\n\ntype Orders interface {\n\tGet()\n}\n\ntype Clock interface {\n\tNow()\n}\n\nfunc F() needs Orders, /* keep: Clock is fixed in tests */ Clock {}\n",
		"paren.ergo":    "package p\nfunc F(f func() needs (/* KEEP */ A, B)) {}\n",
		"provides.ergo": "package p\nwire C provides A, /* KEEP */ B { A = a\n B = b }\n",
		"line.ergo":     "package p\nfunc F() needs A, // KEEP\n\tB {}\n",
	}
	var paths []string
	for name, src := range cases {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, path)
	}
	if code := run(append([]string{"-w"}, paths...)); code != 0 {
		t.Fatalf("migrate196 -w exited %d, want 0 (no notes)", code)
	}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		out := string(data)
		want := "KEEP"
		if strings.HasSuffix(path, "comma.ergo") {
			want = "/* keep: Clock is fixed in tests */"
		}
		if !strings.Contains(out, want) {
			t.Errorf("%s: the written file lost the comment %q:\n%s", filepath.Base(path), want, out)
		}
		if strings.Contains(out, "package p") || !strings.Contains(out, " | ") {
			t.Errorf("%s: the file was not rewritten to the decided syntax:\n%s", filepath.Base(path), out)
		}
		if _, err := syntax.Parse(out); err != nil {
			t.Errorf("%s: the written file does not parse: %v\n%s", filepath.Base(path), err, out)
		}
	}
}
