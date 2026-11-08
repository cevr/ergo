package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRunAndTestPrintFindingsAsText is the acceptance check of T2
// (tl-text-diagnostics), part one: `ergo run` and `ergo test` print a
// source that does not check as `file:line:col: error CODE: message` lines
// on stderr, the lines `ergo diagnostics` prints, then "check failed"; they
// print nothing on stdout and exit 1. With --json they print the check
// envelope on stdout as before. `ergo build` keeps the envelope.
func TestRunAndTestPrintFindingsAsText(t *testing.T) {
	binary := buildTestCLI(t)
	root := physicalTempDir(t)
	cli := func(dir string, args ...string) (string, string, int) {
		t.Helper()
		stdout, stderr, code := runSmokeCLI(t, processDeadline, binary, dir, "", args...)
		return string(stdout), string(stderr), code
	}
	smokeWrite(t, root, "bad.ergo", "func main() needs Console {\n\tConsole.Println(nope)\n}\n\ntest T { assert true }\n")
	smokeWrite(t, root, "mod/ergo.mod", "module app\n")
	smokeWrite(t, root, "mod/lib/x.ergo", "func Value() string {\n\tmissing\n}\n")
	smokeWrite(t, root, "mod/cmd/app/main.ergo", "import app/lib\n\nfunc main() needs Console { Console.Println(lib.Value()) }\n\ntest T { assert lib.Value() == \"\" }\n")
	const golden = "bad.ergo:2:18: error S104: unknown name nope\n"
	for _, c := range []struct {
		dir, source, want string
	}{
		{root, "bad.ergo", golden},
		{filepath.Join(root, "mod"), "cmd/app", ""},
	} {
		listed, _, code := cli(c.dir, "diagnostics", c.source)
		if code != 1 || !strings.Contains(listed, ": error S") || (c.want != "" && listed != c.want) {
			t.Fatalf("ergo diagnostics %s: exit %d stdout %q; want %q", c.source, code, listed, c.want)
		}
		for _, command := range []string{"run", "test"} {
			stdout, stderr, code := cli(c.dir, command, c.source)
			if code != 1 || stdout != "" || stderr != listed+"check failed\n" {
				t.Fatalf("ergo %s %s: exit %d stdout %q stderr %q; want exit 1, no stdout, stderr %q", command, c.source, code, stdout, stderr, listed+"check failed\n")
			}
			stdout, stderr, code = cli(c.dir, command, "--json", c.source)
			envelope := smokeJSON(t, []byte(stdout))
			if code != 1 || envelope["checked"] != false || stderr != "check failed\n" {
				t.Fatalf("ergo %s --json %s: exit %d stdout %q stderr %q", command, c.source, code, stdout, stderr)
			}
		}
		stdout, stderr, code := cli(c.dir, "build", c.source)
		if envelope := smokeJSON(t, []byte(stdout)); code != 1 || envelope["checked"] != false || stderr != "check failed\n" {
			t.Fatalf("ergo build %s: exit %d stdout %q stderr %q", c.source, code, stdout, stderr)
		}
	}
	if _, stderr, code := cli(root, "check", "--json", "bad.ergo"); code != 1 || !strings.Contains(stderr, "--json is only supported by diagnostics, run and test") {
		t.Fatalf("ergo check --json: exit %d stderr %q", code, stderr)
	}
}

// TestFormatFolder is the acceptance check of T2, part two: `ergo fmt
// [--check] DIR` formats every .ergo file of the folder and of the folders
// below it, in lexical order, and skips testdata and folders whose names
// start with "." or "_", as gofmt and the go command walk a tree.
func TestFormatFolder(t *testing.T) {
	binary := buildTestCLI(t)
	root := physicalTempDir(t)
	cli := func(args ...string) (string, string, int) {
		t.Helper()
		stdout, stderr, code := runSmokeCLI(t, processDeadline, binary, root, "", args...)
		return string(stdout), string(stderr), code
	}
	const messy, tidy = "func  f()  int {   1 }\n", "func f() int { 1 }\n"
	for _, name := range []string{"pkg/a.ergo", "pkg/sub/deep/b.ergo", "pkg/testdata/c.ergo", "pkg/.hidden/d.ergo", "pkg/_skip/e.ergo"} {
		smokeWrite(t, root, name, messy)
	}
	smokeWrite(t, root, "pkg/sub/notes.txt", "not source\n")
	smokeWrite(t, root, "empty/readme.txt", "no source\n")
	want := "would reformat " + filepath.Join("pkg", "a.ergo") + "\nwould reformat " + filepath.Join("pkg", "sub", "deep", "b.ergo") + "\n"
	if stdout, stderr, code := cli("fmt", "--check", "pkg"); code != 1 || stdout != "" || stderr != want {
		t.Fatalf("ergo fmt --check pkg: exit %d stdout %q stderr %q; want exit 1 and %q", code, stdout, stderr, want)
	}
	if _, stderr, code := cli("fmt", "pkg"); code != 0 || stderr != strings.ReplaceAll(want, "would reformat", "formatted") {
		t.Fatalf("ergo fmt pkg: exit %d stderr %q", code, stderr)
	}
	for name, text := range map[string]string{"pkg/a.ergo": tidy, "pkg/sub/deep/b.ergo": tidy, "pkg/testdata/c.ergo": messy, "pkg/.hidden/d.ergo": messy, "pkg/_skip/e.ergo": messy} {
		if got := string(smokeRead(t, filepath.Join(root, filepath.FromSlash(name)))); got != text {
			t.Fatalf("%s after ergo fmt pkg: %q, want %q", name, got, text)
		}
	}
	if _, stderr, code := cli("fmt", "--check", "pkg", filepath.Join("pkg", "testdata", "c.ergo")); code != 1 || !strings.Contains(stderr, "already formatted "+filepath.Join("pkg", "a.ergo")) || !strings.Contains(stderr, "would reformat "+filepath.Join("pkg", "testdata", "c.ergo")) {
		t.Fatalf("ergo fmt --check pkg FILE: exit %d stderr %q", code, stderr)
	}
	if _, stderr, code := cli("fmt", "--check", "empty"); code != 2 || !strings.Contains(stderr, "folder empty holds no .ergo file") {
		t.Fatalf("ergo fmt --check empty: exit %d stderr %q", code, stderr)
	}
	if err := os.WriteFile(filepath.Join(root, "pkg", "z.ergo"), []byte("func (\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, code := cli("fmt", "--check", "pkg"); code != 2 {
		t.Fatalf("ergo fmt --check pkg with a syntax error: exit %d, want 2", code)
	}
}
