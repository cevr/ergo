package exec

import (
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"ergo.local/prototype/runtime/rt"

	v1 "ergo.local/prototype/runtime/v1"
)

// TestExecInWritesOnlyTheFolder: a command of the test's folder, run by
// ExecRunIn and started by ExecStartIn, can write in the folder but not at
// a path it computes for itself outside it (the folder's parent, which the
// test itself can write), and holds no capability (TH1b; rows.md, Test's
// ambient 5). The path check cannot see that path: it is the program's
// own. Linux only: elsewhere the path check is all that holds. Mutants
// th1b-run-unsandboxed, th1b-start-unsandboxed, th1b-writable,
// th1b-caps-kept (runtime/v1 ledger).
func TestExecInWritesOnlyTheFolder(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the folder's sandbox is Linux's; elsewhere only the path check holds")
	}
	const script = `out="$(dirname "$PWD")/escape-$1"
echo x > "$out" 2>/dev/null && echo escaped
echo y > kept-$1 || echo refused-inside
grep -q '^CapEff:[[:space:]]*0*$' /proc/self/status || echo capable`
	root := v1.TestFolderOpen()
	defer v1.TestFolderRemove(root)
	task := rt.Root("exec sandbox test")
	check := func(how, out string) {
		t.Helper()
		escape := filepath.Join(filepath.Dir(root), "escape-"+how)
		if _, err := os.Stat(escape); err == nil {
			_ = os.Remove(escape)
			t.Errorf("%s: the program wrote %s, outside the folder", how, escape)
		}
		if out != "" {
			t.Errorf("%s: the program printed %q", how, out)
		}
		if _, err := os.Stat(filepath.Join(root, "kept-"+how)); err != nil {
			t.Errorf("%s: the program could not write in the folder: %v", how, err)
		}
	}
	res, err := ExecRunIn(task, root, "/bin/sh", []string{"-c", script, "sh", "run"}, nil, root, "", 0)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	check("run", res.Stdout)
	running, err := ExecStartIn(task, root, "/bin/sh", []string{"-c", script, "sh", "start"}, nil, root, "", 0, execDiscard, "", false, execDiscard, "", false)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer running.Close(task)
	if status := running.Wait(task); status != 0 {
		t.Errorf("start: status %d", status)
	}
	check("start", "")
}

// TestExecConfineAgreesWithConfine: exec.NewTestHost's check refuses a
// command exactly when its directory, or one of its arguments read
// against that directory, leads out of the folder by Confine, the one
// rule of fs.NewTestHost's paths too, and names the first that does; a
// file sink is refused exactly when Confine refuses it from the folder.
// Seeded: the paths are drawn from words, "..", ".", "/", the folder
// itself and a link inside it that points out. A failing seed prints the
// command. Mutants th1-exec-args and th1-exec-sink (runtime/v1 ledger).
func TestExecConfineAgreesWithConfine(t *testing.T) {
	root := v1.TestFolderOpen()
	defer v1.TestFolderRemove(root)
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
		base, ok := v1.Confine(root, "", dir)
		if !ok {
			want = dir
		} else {
			for _, a := range args {
				if _, in := v1.Confine(root, base, a); !in {
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
		p, in := v1.Confine(root, "", sink)
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
