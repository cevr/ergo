package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// argvProgram prints each program argument on its own line, then "end".
const argvProgram = `import os

func show(args []string) needs Console {
	for a in args {
		Console.Println("[\(a)]")
	}
	Console.Println("end")
}

func main() needs Console | Ambient {
	show(with context { os.Process: os.NewHost() } { os.Process.Args() })
}
`

// TestErgoRunPassesArguments is the acceptance check of T1 (tl-run-args):
// `ergo run FILE|DIR [--] ARGS...` passes ARGS to the program as go run
// does, ergo's options standing before the source, and the build goes to a
// private temporary directory that is removed after the run: no dist/ in
// the working directory, nothing left in TMPDIR. A program folder that was
// run in place still passes scripts/examples, whose layout check refuses an
// unexpected dist/.
func TestErgoRunPassesArguments(t *testing.T) {
	binary := buildTestCLI(t)
	root := physicalTempDir(t)
	xdg := filepath.Join(t.TempDir(), "xdg")
	tmp := t.TempDir()
	program := filepath.Join(root, "examples", "argv")
	smokeWrite(t, program, "ergo.mod", "module argv\n")
	smokeWrite(t, program, "cmd/argv/main.ergo", argvProgram)
	smokeWrite(t, program, "README.md", "# argv\n\nPrints its arguments.\n")
	smokeWrite(t, program, "tags", "default\n")
	smokeWrite(t, program, "cases/plain/expected.out", "end\n")
	smokeWrite(t, program, "cases/flags/args", "-x\ny\n")
	smokeWrite(t, program, "cases/flags/expected.out", "[-x]\n[y]\nend\n")
	file := smokeWrite(t, root, "one/main.ergo", argvProgram)
	entries := func(dir string) []string {
		t.Helper()
		found, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, e := range found {
			names = append(names, e.Name())
		}
		return names
	}
	before := strings.Join(entries(program), " ")
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"run", "cmd/argv", "--", "-x", "y"}, "[-x]\n[y]\nend\n"},
		{[]string{"run", "cmd/argv", "-x"}, "[-x]\nend\n"},
		{[]string{"run", "cmd/argv"}, "end\n"},
		{[]string{"run", "--target", "go", "cmd/argv", "--", "--", "-o"}, "[--]\n[-o]\nend\n"},
		{[]string{"run", "cmd/argv", "--seed", "1", "a b"}, "[--seed]\n[1]\n[a b]\nend\n"},
		{[]string{"run", file, "--", "-x", "y"}, "[-x]\n[y]\nend\n"},
	} {
		stdout, stderr, code := testRunCLI(t, binary, program, xdg, tmp, c.args...)
		if code != 0 || stdout != c.want {
			t.Fatalf("ergo %v: exit %d stdout %q stderr %q; want %q", c.args, code, stdout, stderr, c.want)
		}
		if after := strings.Join(entries(program), " "); after != before {
			t.Fatalf("ergo %v left the working directory with %s; it held %s", c.args, after, before)
		}
		emptyDir(t, tmp, "the temporary directory")
	}
	if names := entries(filepath.Join(root, "one")); len(names) != 1 {
		t.Fatalf("the source folder holds %v after ergo run, want main.ergo only", names)
	}
	repo, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command("go", "run", "./scripts/examples", "-root", root, "-ergo", binary, "argv")
	command.Dir = repo
	command.Env = append(os.Environ(), "XDG_CACHE_HOME="+xdg)
	stdout, stderr, code := runSmokeCommand(t, command)
	if code != 0 || !strings.Contains(string(stdout), "ok argv/flags") || !strings.Contains(string(stdout), "ok argv/plain") {
		t.Fatalf("scripts/examples argv after ergo run in its folder: exit %d\n%s\n%s", code, stdout, stderr)
	}
}
