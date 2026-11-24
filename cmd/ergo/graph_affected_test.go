package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	lgraph "ergo.local/prototype/internal/linked/graph"
)

// TestGraphAffectedReadsTheBaseRevision runs `ergo graph affected HEAD`
// in process on a git repository of two modules: app requires lib through
// a go.mod replace. After an edit of lib.Size the base's rows come from
// the committed tree, which holds app's module and the module its replace
// names (baseFolders), and the answer is the edited declaration and its
// dependents in app; both go to the query as columns, as the binary hands
// them over. Mutant graph-base-skips-replaces
// (internal/sketch/lower/testdata/mutants.json): an archive of app's
// module alone cannot check app at base.
func TestGraphAffectedReadsTheBaseRevision(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	repo := t.TempDir()
	write := func(name, text string) {
		p := filepath.Join(repo, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("lib/ergo.mod", "module example.com/lib\n")
	write("lib/go.mod", "module example.com/lib\n\ngo 1.27\n")
	write("lib/lib.ergo", "func Size() int { 1 }\n\nfunc Other() int { 2 }\n")
	write("app/ergo.mod", "module app\n")
	write("app/go.mod", "module app\n\ngo 1.27\n\nrequire example.com/lib v0.0.0\n\nreplace example.com/lib => ../lib\n")
	write("app/count/count.ergo", "import example.com/lib\n\nfunc Count() int { lib.Size() + 1 }\n")
	write("app/other/other.ergo", "import example.com/lib\n\nfunc Keep() int { lib.Other() }\n")
	git := func(args ...string) {
		cmd := exec.Command("git", append([]string{"--no-optional-locks"}, args...)...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t", "GIT_CONFIG_GLOBAL=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	git("add", ".")
	git("commit", "-q", "-m", "base")
	write("lib/lib.ergo", "func Size() int { 3 }\n\nfunc Other() int { 2 }\n")
	app := filepath.Join(repo, "app") + "/..."
	rows, err := graphRows(app)
	if err != nil {
		t.Fatal(err)
	}
	old, _, err := baseRows(app, "HEAD")
	if err != nil || old == nil {
		t.Fatalf("the base's rows: %v", err)
	}
	strs, ints := rows.AppendColumns(nil, nil)
	strs, ints = old.AppendColumns(strs, ints)
	out, err := lgraph.RunColumns(context.Background(), []string{"affected", "HEAD", "--json"}, strs, ints)
	if err != nil || len(out) != 3 || out[0] != "0" {
		t.Fatalf("ergo graph affected HEAD: %v %q", err, out)
	}
	var reached []struct{ Node, Kind, By string }
	if err := json.Unmarshal([]byte(out[1]), &reached); err != nil {
		t.Fatalf("%v: %s", err, out[1])
	}
	var names []string
	for _, r := range reached {
		names = append(names, r.Node)
	}
	if len(names) == 0 || names[0] != "example.com/lib.Size" || !slices.Contains(names, "app/count.Count") || slices.Contains(names, "app/other.Keep") || slices.Contains(names, "example.com/lib.Other") {
		t.Fatalf("affected by the edit of lib.Size: %v", names)
	}
}

// TestGraphCompareReadsBaseRevision exercises `ergo graph --in DIR/...
// compare HEAD --json` end-to-end on a git repository: self-comparison
// reports no regression; adding a long file reports advisory regressed
// true with exit 0 (non-blocking); breaking type-checking in head refuses
// the comparison with an error.
func TestGraphCompareReadsBaseRevision(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	repo := t.TempDir()
	write := func(name, text string) {
		p := filepath.Join(repo, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("ergo.mod", "module app\n")
	write("pkg/pkg.ergo", "func A() int { 1 }\nfunc B() int { 2 }\n")
	write("cmd/app/main.ergo", "import app/pkg\n\nfunc main() needs Console {\n\tConsole.Println(\"\\(pkg.A() + pkg.B())\")\n}\n")
	git := func(args ...string) {
		cmd := exec.Command("git", append([]string{"--no-optional-locks"}, args...)...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t", "GIT_CONFIG_GLOBAL=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	git("add", ".")
	git("commit", "-q", "-m", "base")

	target := repo + "/..."
	// 1. Self-comparison: no regression.
	headRows, err := graphRowsChecked(target)
	if err != nil {
		t.Fatal(err)
	}
	oldRows, _, err := baseRowsChecked(target, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	strs, ints := headRows.AppendColumns(nil, nil)
	strs, ints = oldRows.AppendColumns(strs, ints)
	out, err := lgraph.RunColumns(context.Background(), []string{"compare", "HEAD", "--max-lines", "3", "--json"}, strs, ints)
	if err != nil || len(out) != 3 || out[0] != "0" {
		t.Fatalf("self compare: %v %q", err, out)
	}
	var cmp struct {
		Advisory  bool
		Regressed bool
		Added     []struct{ Rule, Subject, Key string }
		Grown     []struct{ Rule, Subject, Key string }
		Unchanged []struct{ Rule, Subject, Key string }
		Removed   []struct{ Rule, Subject, Key string }
	}
	if err := json.Unmarshal([]byte(out[1]), &cmp); err != nil {
		t.Fatalf("%v: %s", err, out[1])
	}
	if !cmp.Advisory || cmp.Regressed || len(cmp.Added) != 0 || len(cmp.Grown) != 0 || len(cmp.Removed) != 0 {
		t.Fatalf("self compare unexpected: %+v", cmp)
	}

	// 2. Regression: lengthen pkg/pkg.ergo beyond 3 lines; advisory exit stays 0.
	write("pkg/pkg.ergo", "func A() int { 1 }\nfunc B() int { 2 }\nfunc C() int { 3 }\nfunc D() int { 4 }\n")
	headRows, err = graphRowsChecked(target)
	if err != nil {
		t.Fatal(err)
	}
	strs, ints = headRows.AppendColumns(nil, nil)
	strs, ints = oldRows.AppendColumns(strs, ints)
	out, err = lgraph.RunColumns(context.Background(), []string{"compare", "HEAD", "--max-lines", "3", "--json"}, strs, ints)
	if err != nil || len(out) != 3 || out[0] != "0" {
		t.Fatalf("regressed compare must stay advisory (exit 0): %v %q", err, out)
	}
	if err := json.Unmarshal([]byte(out[1]), &cmp); err != nil {
		t.Fatalf("%v: %s", err, out[1])
	}
	if !cmp.Advisory || !cmp.Regressed || len(cmp.Added) != 1 || cmp.Added[0].Rule != "long-file" {
		t.Fatalf("regressed compare unexpected: %+v", cmp)
	}

	// 3. Unchecked head must be refused.
	write("pkg/pkg.ergo", "func A() int { \"not an int\" }\n")
	if _, err := graphRowsChecked(target); err == nil {
		t.Fatal("expected unchecked head to be refused by graphRowsChecked")
	}
}
