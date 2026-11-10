package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestModuleCommands is the smallest repro of E5: a law in a dependency must
// fail the module command, and fixing it must make the command pass. It builds
// Go because in-process checking cannot observe the CLI's exit status or run
// the generated law bodies. One CLI binary serves every invocation, and the
// smoke runner bounds each child's memory and owns its process group.
// It kills e5-pattern, e5-roots, e5-order, e5-status, e5-continue, e5-nested
// e5-fmt, e5-fatal and e5-empty; previous CLI tests select one package,
// never a module pattern.
func TestModuleCommands(t *testing.T) {
	if testing.Short() {
		t.Skip("module command process acceptance runs in the full tier")
	}
	bin := buildTestCLI(t)
	root := physicalTempDir(t)
	write := func(name, text string) { smokeWrite(t, root, name, text) }
	write("ergo.mod", "module tree\n")
	write("a/a.ergo", "import tree/z\nfunc Value(x int) int { z.Value(x) }\ntest FromA { assert Value(3) == 3 }\n")
	write("b/b.ergo", "test Independent { assert true }\n")
	z := "func Value(x int) int { x }\nlaw Identity(x int) { Value(x) != x }\n"
	write("z/z.ergo", z)
	for _, ignored := range []string{".hidden", "_ignored", "testdata", "nested"} {
		write(filepath.Join(ignored, "bad.ergo"), "this is not Ergo\n")
	}
	write("nested/ergo.mod", "module separate\n")
	write("nested/deep/bad.ergo", "this is not Ergo\n")
	run := func(args ...string) (string, string, int) {
		t.Helper()
		out, err, code := runSmokeCLI(t, bin, root, "", args...)
		return string(out), string(err), code
	}
	order := func(out string) {
		t.Helper()
		z, a, b := strings.Index(out, "package tree/z\n"), strings.Index(out, "package tree/a\n"), strings.Index(out, "package tree/b\n")
		if z < 0 || a <= z || b <= a {
			t.Fatalf("packages not run in dependency order: %s", out)
		}
	}
	out, err, code := run("test", "./...", "--seed", "7")
	if code != 1 || !strings.Contains(out, "FAIL law Identity") || !strings.Contains(out, "ok FromA") || !strings.Contains(out, "ok Independent") {
		t.Fatalf("failing module law: exit %d\n%s\n%s", code, out, err)
	}
	order(out)
	write("z/z.ergo", strings.Replace(z, "!=", "==", 1))
	out, err, code = run("test", "./...", "--seed=7")
	if code != 0 || !strings.Contains(out, "ok law Identity") || !strings.Contains(out, "ok Independent") {
		t.Fatalf("fixed module law: exit %d\n%s\n%s", code, out, err)
	}
	order(out)
	if out, err, code = run("check", "./..."); code != 0 {
		t.Fatalf("fixed module check: exit %d\n%s\n%s", code, out, err)
	}
	write("b/b.ergo", "func Bad()int{\"not an int\"}\n")
	out, err, code = run("check", "./...")
	if code != 1 || !strings.Contains(out, `"checked":false`) || !strings.Contains(out, `"checked":true`) || !strings.Contains(out, "b/b.ergo") {
		t.Fatalf("module check missed an independent package: exit %d\n%s\n%s", code, out, err)
	}
	write("z/z.ergo", "func Value(x int)int{x}\nlaw Identity(x int){Value(x)==x}\n")
	out, err, code = run("fmt", "--check", "./...")
	if code != 1 || !strings.Contains(out+err, "z.ergo") || !strings.Contains(out+err, "b.ergo") {
		t.Fatalf("module formatting check: exit %d\n%s\n%s", code, out, err)
	}
	if strings.Index(out+err, "z.ergo") > strings.Index(out+err, "a.ergo") {
		t.Fatalf("format reports are not in dependency order:\n%s\n%s", out, err)
	}
	// Formatting stays syntax-only even when b's result type is wrong.
	if out, err, code = run("fmt", "./..."); code != 0 {
		t.Fatalf("format module: exit %d\n%s\n%s", code, out, err)
	}
	if out, err, code = run("fmt", "./...", "--check"); code != 0 {
		t.Fatalf("formatted module: exit %d\n%s\n%s", code, out, err)
	}
	if out, err, code = run("test", "b/..."); code != 1 || !strings.Contains(err, "b.ergo") || strings.Contains(out, "Identity") {
		t.Fatalf("subtree pattern: exit %d\n%s\n%s", code, out, err)
	}
	write("empty/README", "no Ergo source\n")
	if out, err, code = run("test", "empty/..."); code != 2 || !strings.Contains(err, "no Ergo packages") {
		t.Fatalf("empty pattern: exit %d\n%s\n%s", code, out, err)
	}
	write("b/b.ergo", "test Independent { assert true }\n")
	write("z/z.ergo", "func Value(x int) int { x }\nfunc at(xs []int, i int) int { xs[i] }\ntest Breaks { assert at([]int{}, 1) == 0 }\n")
	if out, err, code = run("test", "./..."); code != 2 || !strings.Contains(out, "FAIL Breaks") || strings.Contains(out, "package tree/a") {
		t.Fatalf("module defect must stop later packages: exit %d\n%s\n%s", code, out, err)
	}
}
