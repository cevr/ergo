package ergobuild

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestToolBuildsTheSameBytesInEveryCheckout: the same sources, in two
// checkouts at two folders and two commits, build the same bytes with
// Command, so every workspace of a repository runs one compiler identity
// and shares the answers stored under it. It builds a Go program because
// the claim is about the go command's output: no in-process check sees
// the folder or the commit a binary records. The program is a small one
// that names its own source file, not ergo, so the two builds take
// seconds. Mutants: ergobuild-drops-trimpath (the folders differ in the
// binary), ergobuild-drops-buildvcs-off (the commits differ in it).
func TestToolBuildsTheSameBytesInEveryCheckout(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatalf("git: %v", err)
	}
	var built [2][]byte
	for i, message := range []string{"first checkout", "second checkout"} {
		dir := filepath.Join(t.TempDir(), "checkout", message)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(dir, "go.mod"), "module example.com/probe\n\ngo 1.27\n")
		write(t, filepath.Join(dir, "main.go"), "package main\n\nimport (\n\t\"fmt\"\n\t\"runtime\"\n)\n\nfunc main() {\n\t_, file, _, _ := runtime.Caller(0)\n\tfmt.Println(file)\n}\n")
		for _, args := range [][]string{{"init", "-q"}, {"add", "."}, {"-c", "user.name=probe", "-c", "user.email=probe@example.com", "commit", "-q", "-m", message}} {
			git := exec.Command("git", args...)
			git.Dir = dir
			if out, err := git.CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v\n%s", args, err, out)
			}
		}
		out := filepath.Join(t.TempDir(), "probe")
		build := Command(out, ".")
		build.Dir = dir
		if text, err := build.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", build.Args, err, text)
		}
		data, err := os.ReadFile(out)
		if err != nil {
			t.Fatal(err)
		}
		built[i] = data
	}
	if !bytes.Equal(built[0], built[1]) {
		t.Fatalf("one source tree built two binaries (%d and %d bytes) in two checkouts with %v: each workspace would run its own compiler identity", len(built[0]), len(built[1]), Flags)
	}
}

func write(t *testing.T, name, text string) {
	t.Helper()
	if err := os.WriteFile(name, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}
