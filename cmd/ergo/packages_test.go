package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestErgoPackages is the acceptance check of 196b U3 (pkg-load): source
// on disk becomes a program of packages through `ergo check|build|run|
// test DIR`. A folder is a package (Q2), ergo.mod marks the module root and
// names the module (Q1), an import cycle is S108 (Q4), a std path always
// names std, and every diagnostic names its file relative to the module
// root (Q10). The fixtures are cmd/ergo/testdata/packages, copied into a
// private workspace, since a run writes dist/.
func TestErgoPackages(t *testing.T) {
	binary := buildTestCLI(t)
	workspace := smokeWorkspace(t, "cmd/ergo/testdata/packages")
	fixtures := filepath.Join(workspace, "cmd", "ergo", "testdata", "packages")
	cli := func(t *testing.T, dir string, args ...string) (string, string, int) {
		t.Helper()
		stdout, stderr, code := runSmokeCLI(t, binary, dir, "", args...)
		return string(stdout), string(stderr), code
	}

	t.Run("two files in one folder", func(t *testing.T) {
		stdout, stderr, code := cli(t, fixtures, "run", "twofiles")
		if code != 0 || stdout != "b\n" {
			t.Fatalf("ergo run twofiles: exit %d stdout %q, want b\n%s", code, stdout, stderr)
		}
	})

	t.Run("a module imports its package by path", func(t *testing.T) {
		dir := filepath.Join(fixtures, "module")
		stdout, stderr, code := cli(t, dir, "run", "cmd/app")
		if code != 0 || stdout != "lib value\n" {
			t.Fatalf("ergo run cmd/app: exit %d stdout %q, want lib value\n%s", code, stdout, stderr)
		}
		stdout, stderr, code = cli(t, dir, "build", "cmd/app")
		if code != 0 || stdout != filepath.Join("dist", "app")+"\n" {
			t.Fatalf("ergo build cmd/app: exit %d stdout %q\n%s", code, stdout, stderr)
		}
		stdout, stderr, code = cli(t, dir, "check", "cmd/app")
		if code != 0 || !strings.Contains(stdout, `"checked":true`) {
			t.Fatalf("ergo check cmd/app: exit %d stdout %q\n%s", code, stdout, stderr)
		}
	})

	t.Run("an import cycle is S108", func(t *testing.T) {
		stdout, stderr, code := cli(t, filepath.Join(fixtures, "cycle"), "check", "a")
		const want = "import cycle: cyc/a imports cyc/b imports cyc/a"
		if code != 1 || !strings.Contains(stdout, `"code":"S108"`) || !strings.Contains(stdout, want) || !strings.Contains(stdout, `"file":"b/b.ergo"`) {
			t.Fatalf("ergo check a: exit %d stdout %s\n%s\nwant S108 %q in b/b.ergo", code, stdout, stderr, want)
		}
	})

	t.Run("a std path names std; the module prefix names the folder", func(t *testing.T) {
		dir := filepath.Join(fixtures, "shadow")
		stdout, stderr, code := cli(t, dir, "run", "cmd/local")
		if code != 0 || stdout != "local log\n" {
			t.Fatalf("ergo run cmd/local: exit %d stdout %q, want local log\n%s", code, stdout, stderr)
		}
		stdout, stderr, code = cli(t, dir, "check", "cmd/std")
		if code != 1 || !strings.Contains(stdout, `"code":"S104"`) || !strings.Contains(stdout, "Hello") {
			t.Fatalf("ergo check cmd/std: exit %d stdout %s\n%s\nwant S104 for std log.Hello", code, stdout, stderr)
		}
		stdout, stderr, code = cli(t, filepath.Join(fixtures, "badmod"), "check", ".")
		if code != 1 || !strings.Contains(stdout, `"code":"S108"`) || !strings.Contains(stdout, `"file":"ergo.mod"`) || !strings.Contains(stdout, "std package log") {
			t.Fatalf("ergo check badmod: exit %d stdout %s\n%s\nwant S108 in ergo.mod naming std package log", code, stdout, stderr)
		}
	})

	t.Run("a nested ergo.mod ends the module", func(t *testing.T) {
		dir := filepath.Join(fixtures, "nested")
		stdout, stderr, code := cli(t, dir, "run", "cmd/app")
		const want = `error S104: unknown package "app/sub/x": the folder sub is module other (sub/ergo.mod)`
		if code != 1 || stdout != "" || !strings.Contains(stderr, want) {
			t.Fatalf("ergo run cmd/app: exit %d stdout %s\n%s\nwant S104 %s", code, stdout, stderr, want)
		}
		// Inside the nested module the same folder is its own package.
		stdout, stderr, code = cli(t, filepath.Join(dir, "sub"), "run", "cmd/other")
		if code != 0 || stdout != "nested x\n" {
			t.Fatalf("ergo run sub/cmd/other: exit %d stdout %q, want nested x\n%s", code, stdout, stderr)
		}
	})

	t.Run("a folder that is not a name needs an alias", func(t *testing.T) {
		dir := filepath.Join(fixtures, "alias")
		stdout, stderr, code := cli(t, dir, "run", "cmd/good")
		if code != 0 || stdout != "okra\n" {
			t.Fatalf("ergo run cmd/good: exit %d stdout %q, want okra\n%s", code, stdout, stderr)
		}
		stdout, stderr, code = cli(t, dir, "check", "cmd/bad")
		if code != 1 || !strings.Contains(stdout, `"code":"S108"`) || !strings.Contains(stdout, "import okrarepo my-app/okra-repo") {
			t.Fatalf("ergo check cmd/bad: exit %d stdout %s\n%s\nwant S108 naming the alias form", code, stdout, stderr)
		}
	})

	t.Run("a diagnostic names its file", func(t *testing.T) {
		dir := filepath.Join(fixtures, "diag")
		stdout, stderr, code := cli(t, dir, "diagnostics", "--json", "cmd/app")
		if code != 1 {
			t.Fatalf("ergo diagnostics --json cmd/app: exit %d\n%s\n%s", code, stdout, stderr)
		}
		var report struct {
			Diagnostics []struct {
				Code              string `json:"code"`
				File              string `json:"file"`
				LocationAvailable bool   `json:"locationAvailable"`
				LSP               struct {
					Range struct {
						Start struct{ Line, Character int }
					}
				} `json:"lsp"`
			} `json:"diagnostics"`
		}
		if err := json.Unmarshal([]byte(stdout), &report); err != nil {
			t.Fatalf("report %s: %v", stdout, err)
		}
		if len(report.Diagnostics) != 1 {
			t.Fatalf("findings %+v, want one", report.Diagnostics)
		}
		d := report.Diagnostics[0]
		if d.Code != "S104" || d.File != "lib/x.ergo" || !d.LocationAvailable || d.LSP.Range.Start.Line != 1 || d.LSP.Range.Start.Character != 8 {
			t.Fatalf("finding %+v, want S104 in lib/x.ergo at 2:9", d)
		}
		stdout, _, code = cli(t, dir, "diagnostics", "cmd/app")
		if code != 1 || !strings.HasPrefix(stdout, "lib/x.ergo:2:9: error S104: ") {
			t.Fatalf("ergo diagnostics cmd/app: exit %d stdout %q, want lib/x.ergo:2:9", code, stdout)
		}
	})

	t.Run("an edit of an imported package rebuilds", func(t *testing.T) {
		dir := t.TempDir()
		smokeWrite(t, dir, "ergo.mod", "module app\n")
		smokeWrite(t, dir, "cmd/app/main.ergo", string(smokeRead(t, filepath.Join(fixtures, "module", "cmd", "app", "main.ergo"))))
		lib := smokeWrite(t, dir, "lib/lib.ergo", "let Value string = \"first\"\n")
		stdout, stderr, code := cli(t, dir, "run", "cmd/app")
		if code != 0 || stdout != "first\n" {
			t.Fatalf("first run: exit %d stdout %q\n%s", code, stdout, stderr)
		}
		if err := os.WriteFile(lib, []byte("let Value string = \"second\"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		stdout, stderr, code = cli(t, dir, "run", "cmd/app")
		if code != 0 || stdout != "second\n" {
			t.Fatalf("second run after the edit: exit %d stdout %q, want second\n%s", code, stdout, stderr)
		}
	})

	t.Run("scripts/examples runs tidewalk-replay", func(t *testing.T) {
		root, err := filepath.Abs(filepath.Join("..", ".."))
		if err != nil {
			t.Fatal(err)
		}
		command := exec.Command("go", "run", "./scripts/examples", "-ergo", binary, "tidewalk-replay")
		command.Dir = root
		stdout, stderr, code := runSmokeCommand(t, command)
		if code != 0 || !strings.Contains(string(stdout), "ok tidewalk-replay/basic") {
			t.Fatalf("scripts/examples tidewalk-replay: exit %d\n%s\n%s", code, stdout, stderr)
		}
	})
}
