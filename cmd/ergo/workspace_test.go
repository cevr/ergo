package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCheckAtAWorkspaceRoot is WS1's command check (MONOREPO §7.2):
// `ergo check ROOT/...` at a folder no module holds checks every member's
// packages once, each member as its own module (today it fails with
// "recursive packages require an ergo.mod module"), and `ergo graph export
// ROOT/... --json` holds a module row for each member, the one with no
// package too, and a requires row for the local replace. The selection
// itself is TestWorkspaceDiscoveryAgrees's property; this is the smallest
// tree that runs it through the command. A vendor folder inside a module
// is left out of ./... (one admission rule), yet the package named by its
// folder still checks. It kills ws-graph-no-module-rows and
// ws-graph-requires-dropped.
func TestCheckAtAWorkspaceRoot(t *testing.T) {
	root := t.TempDir()
	write := func(name, text string) {
		t.Helper()
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("lib/ergo.mod", "module lib\n")
	write("lib/go.mod", "module example.com/lib\n\ngo 1.27\n")
	write("lib/text/libtext.ergo", "func Shout(s string) string { s + \"!\" }\n")
	write("lib/num/libnum.ergo", "func Twice(n int) int { n * 2 }\n")
	write("app/ergo.mod", "module app\n")
	write("app/go.mod", "module example.com/app\n\ngo 1.27\n\nreplace example.com/lib => ../lib\n")
	write("app/core/appcore.ergo", "func One() int { 1 }\n")
	write("empty/ergo.mod", "module empty\n")
	write("app/testdata/fixture/ergo.mod", "module fixture\n")
	write("app/testdata/fixture/f/fixture.ergo", "this is not Ergo\n")
	write("app/vendor/copied/copied.ergo", "func Copied() int { 2 }\n")

	out := captureCommand(t, root, func() error { return checkCommand([]string{root + "/..."}) })
	// One envelope per package; a package's revision is its source's, so
	// three distinct revisions are the three packages, each once.
	revisions := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		var env struct {
			Checked  bool   `json:"checked"`
			Revision string `json:"revision"`
		}
		if err := json.Unmarshal([]byte(line), &env); err != nil || !env.Checked {
			t.Fatalf("ergo check ROOT/... line %q: %v", line, err)
		}
		revisions[env.Revision] = true
	}
	if n := strings.Count(out, "\n"); n != 3 || len(revisions) != 3 {
		t.Errorf("ergo check ROOT/... printed %d envelopes of %d packages, want 3 packages, each once:\n%s", n, len(revisions), out)
	}
	if strings.Contains(out, "fixture") {
		t.Errorf("ergo check ROOT/... entered testdata:\n%s", out)
	}
	named := captureCommand(t, root, func() error { return checkCommand([]string{filepath.Join(root, "app", "vendor", "copied")}) })
	if !strings.Contains(named, `"checked":true`) {
		t.Errorf("ergo check of a package named under vendor/ did not check it:\n%s", named)
	}

	rows := captureCommand(t, root, func() error { return graphCommand([]string{"export", root + "/...", "--json"}) })
	var g struct {
		Nodes []struct {
			Kind, Name string
		} `json:"nodes"`
		Edges []struct {
			Kind     string
			From, To int
		} `json:"edges"`
	}
	if err := json.Unmarshal([]byte(rows), &g); err != nil {
		t.Fatalf("graph rows: %v\n%s", err, rows)
	}
	modules := map[int]string{}
	for i, n := range g.Nodes {
		if n.Kind == "module" {
			modules[i] = n.Name
		}
	}
	var names []string
	for _, n := range modules {
		names = append(names, n)
	}
	for _, want := range []string{"app", "empty", "lib"} {
		if !strings.Contains(strings.Join(names, " "), want) {
			t.Errorf("graph export at a root has the module rows %v, want app, empty and lib", names)
		}
	}
	requires := 0
	for _, e := range g.Edges {
		if e.Kind == "requires" {
			requires++
			if modules[e.From] != "app" || modules[e.To] != "lib" {
				t.Errorf("requires %s -> %s, want app -> lib", modules[e.From], modules[e.To])
			}
		}
	}
	if requires != 1 {
		t.Errorf("graph export at a root has %d requires rows, want 1:\n%s", requires, rows)
	}
}

// TestLintAtAWorkspaceRootNamesADivergentRequire: `ergo lint ROOT/...` at
// a workspace root reports a member that requires another member's Go
// module by version with no replace line, naming both, the version, the
// go.mod line and the replace that fixes it, and fails; with the replace
// it reports nothing and lints every package. TestWorkspaceDiscoveryAgrees
// holds which requirements diverge; this is the command's report. It
// kills ws-lint-divergence-unreported.
func TestLintAtAWorkspaceRootNamesADivergentRequire(t *testing.T) {
	root := t.TempDir()
	write := func(name, text string) {
		t.Helper()
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("lib/ergo.mod", "module lib\n")
	write("lib/go.mod", "module example.com/lib\n\ngo 1.27\n")
	write("lib/num/libnum.ergo", "func Twice(n int) int { n * 2 }\n")
	write("app/ergo.mod", "module app\n")
	write("app/go.mod", "module example.com/app\n\ngo 1.27\n\nrequire example.com/lib v1.4.0\n")
	write("app/core/appcore.ergo", "func One() int { 1 }\n")
	lint := func() (string, error) {
		t.Helper()
		f, err := os.CreateTemp(t.TempDir(), "stdout-")
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		saved := os.Stdout
		os.Stdout = f
		err = lintCommand([]string{root + "/..."})
		os.Stdout = saved
		b, readErr := os.ReadFile(f.Name())
		if readErr != nil {
			t.Fatal(readErr)
		}
		return string(b), err
	}
	out, err := lint()
	want := "app/go.mod:5: workspace-divergent-require: app requires example.com/lib v1.4.0 by version, but example.com/lib is the member lib beside it"
	if !strings.Contains(out, want) || !strings.Contains(out, "add replace example.com/lib => ../lib") {
		t.Errorf("ergo lint ROOT/... with a divergent require printed:\n%s\nwant a line with %q and its replace", out, want)
	}
	if exitCode(err) != 1 {
		t.Errorf("ergo lint ROOT/... with a divergent require: %v, want exit 1", err)
	}
	write("app/go.mod", "module example.com/app\n\ngo 1.27\n\nrequire example.com/lib v1.4.0\n\nreplace example.com/lib => ../lib\n")
	out, err = lint()
	if err != nil || strings.Contains(out, "workspace-divergent-require") {
		t.Errorf("ergo lint ROOT/... with the replace: %v\n%s", err, out)
	}
}

// captureCommand is what run prints on stdout.
func captureCommand(t *testing.T, dir string, run func() error) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "stdout-")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	saved := os.Stdout
	os.Stdout = f
	err = run()
	os.Stdout = saved
	if err != nil {
		t.Fatalf("in %s: %v", dir, err)
	}
	if _, err := f.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
