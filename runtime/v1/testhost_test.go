package v1

import (
	"math/rand/v2"
	"os"
	"path/filepath"
	"testing"
)

// TestExecConfineAgreesWithConfine: exec.NewTestHost's check refuses a
// command exactly when its directory, or one of its arguments read
// against that directory, leads out of the folder by Confine, the one
// rule of fs.NewTestHost's paths too, and names the first that does; a
// file sink is refused exactly when Confine refuses it from the folder.
// Seeded: the paths are drawn from words, "..", ".", "/", the folder
// itself and a link inside it that points out. A failing seed prints the
// command. Mutants th1-exec-args and th1-exec-sink (runtime/v1 ledger).
func TestExecConfineAgreesWithConfine(t *testing.T) {
	root := TestFolderOpen()
	defer TestFolderRemove(root)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "out")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	parts := []string{"a", "sub", "..", ".", "out", "-rf", "--", root, "/", "x y"}
	r := rand.New(rand.NewPCG(3, 9))
	path := func() string {
		n := r.IntN(4)
		p := ""
		for i := range n {
			if i > 0 {
				p += "/"
			}
			p += parts[r.IntN(len(parts))]
		}
		return p
	}
	for i := range 2000 {
		dir := ""
		if r.IntN(3) == 0 {
			dir = path()
		}
		args := make([]string, r.IntN(4))
		for k := range args {
			args[k] = path()
		}
		want := ""
		base, ok := Confine(root, "", dir)
		if !ok {
			want = dir
		} else {
			for _, a := range args {
				if _, in := Confine(root, base, a); !in {
					want = a
					break
				}
			}
		}
		got, err := ExecConfine(root, dir, args)
		d, denied := err.(ExecDenied)
		switch {
		case want == "" && (err != nil || got != base):
			t.Fatalf("case %d: dir %q args %q: %q, %v; want %q allowed", i, dir, args, got, err, base)
		case want != "" && (!denied || d.Name != want):
			t.Fatalf("case %d: dir %q args %q: %q, %v; want Denied of %q", i, dir, args, got, err, want)
		}
		sink := path()
		p, in := Confine(root, "", sink)
		gotSink, err := ExecConfineSink(root, sink)
		switch {
		case sink == "" && (gotSink != "" || err != nil):
			t.Fatalf("case %d: the empty sink: %q, %v", i, gotSink, err)
		case sink != "" && in && (gotSink != p || err != nil):
			t.Fatalf("case %d: sink %q: %q, %v; want %q", i, sink, gotSink, err, p)
		case sink != "" && !in && err == nil:
			t.Fatalf("case %d: sink %q leads out and is allowed as %q", i, sink, gotSink)
		}
	}
}
