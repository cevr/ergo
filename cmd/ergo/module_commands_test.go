package main

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestModuleLawSelectionReceipts generates packages with distinct laws and
// compares recursive selection and per-package receipts with that model.
// It kills e5-receipt-last-wins and e5-law-every-package, which the existing
// module command test cannot: it uses neither selection nor receipts.
// It builds Go because only the CLI's emitted law runners write receipts and
// report exit status. The smoke runner owns and bounds every child.
func TestModuleLawSelectionReceipts(t *testing.T) {
	if testing.Short() {
		t.Skip("module law process acceptance runs in the full tier")
	}
	bin := buildTestCLI(t)
	for _, seed := range []uint64{7, 19} {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			r := rand.New(rand.NewPCG(seed, seed+1))
			root := physicalTempDir(t)
			smokeWrite(t, root, "ergo.mod", "module tree\n")
			n := 2 + r.IntN(3)
			bad := r.IntN(n - 1) // the refutation is never the last receipt
			var sources strings.Builder
			for i := range n {
				body := "true"
				if i == bad {
					body = "false"
				}
				name, src := fmt.Sprintf("p%d/law.ergo", i), fmt.Sprintf("law Rule%d(b bool) { %s }\n", i, body)
				smokeWrite(t, root, name, src)
				fmt.Fprintf(&sources, "// %s\n%s\n", name, src)
			}
			t.Logf("seed %d\n%sreplay: go test ./cmd/ergo -count=1 -run '^TestModuleLawSelectionReceipts/%d$'", seed, sources.String(), seed)
			run := func(args ...string) (string, string, int) {
				t.Helper()
				out, err, code := runSmokeCLI(t, bin, root, "", args...)
				return string(out), string(err), code
			}
			receipts := filepath.Join(root, "receipts")
			out, stderr, code := run("test", "./...", "--seed", fmt.Sprint(seed), "--receipt", receipts)
			if code != 1 {
				t.Fatalf("seed %d: refuted module: exit %d\n%s\n%s", seed, code, out, stderr)
			}
			for i := range n {
				path := filepath.Join(receipts, "tree", fmt.Sprintf("p%d.json", i))
				data, err := os.ReadFile(path)
				var got struct {
					Source string                                         `json:"source"`
					Laws   []struct{ Run, Status, Counterexample string } `json:"laws"`
				}
				if err != nil || json.Unmarshal(data, &got) != nil {
					t.Errorf("seed %d: receipt %s: %v\n%s\nmodule output:\n%s\n%s", seed, path, err, data, out, stderr)
					continue
				}
				want := "proved"
				if i == bad {
					want = "refuted"
				}
				if got.Source != filepath.Join(root, fmt.Sprintf("p%d", i)) || len(got.Laws) != 1 || got.Laws[0].Run != fmt.Sprintf("law Rule%d", i) || got.Laws[0].Status != want || (i == bad && got.Laws[0].Counterexample == "") {
					t.Fatalf("seed %d: lost package %d verdict %s: %s", seed, i, want, data)
				}
			}
			selected := n - 1
			out, stderr, code = run("test", "./...", "--law", fmt.Sprintf("Rule%d", selected))
			if code != 0 || !strings.Contains(out, fmt.Sprintf("ok law Rule%d", selected)) || strings.Contains(out, "FAIL") || strings.Contains(out, fmt.Sprintf("ok law Rule%d", bad)) {
				t.Fatalf("seed %d: selected law: exit %d\n%s\n%s", seed, code, out, stderr)
			}
			out, stderr, code = run("test", "./...", "--law", "Absent")
			if code != 1 || !strings.Contains(out+stderr, "no law Absent") || strings.Count(out+stderr, "no law Absent") != 1 {
				t.Fatalf("seed %d: unmatched selection: exit %d\n%s\n%s", seed, code, out, stderr)
			}
		})
	}
}

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
