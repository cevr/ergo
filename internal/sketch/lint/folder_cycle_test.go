package lint_test

import (
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"ergo.local/prototype/internal/sketch/lint"
	"ergo.local/prototype/internal/sketchfront"
)

func folderCycleConf(t *testing.T) *lint.Configuration {
	t.Helper()
	settings := map[string]lint.Setting{}
	for _, r := range lint.Builtin().Rules() {
		settings[r.Name] = lint.Setting{Severity: lint.Off, HasSeverity: true}
	}
	settings["sibling-folder-cycle"] = lint.Setting{}
	conf, err := lint.Builtin().Configure(lint.Config{Version: lint.ConfigVersion, Rules: settings}, []string{lint.Layout})
	if err != nil {
		t.Fatal(err)
	}
	return conf
}

// TestSiblingFolderCycleAgreesWithReference generates four acyclic
// packages a/p, a/q, b/p, b/q with a/p -> b/q and optionally b/p -> a/q
// (in production or only in a test), plus an ancestor-only edge
// sub -> sub/leaf as a control. When both directions are production, the
// package graph is acyclic while the folder quotient under `.` has one
// SCC {a, b}. Mutant: layout-folder-tests-missing-quotient (omits the
// LCA lift and loses this witness).
func TestSiblingFolderCycleAgreesWithReference(t *testing.T) {
	hits, clean := 0, 0
	for _, seed := range propertySeeds(16) {
		r := rand.New(rand.NewPCG(seed, 0x0f01))
		mode := r.IntN(3) // 0: cycle, 1: acyclic, 2: test-only return
		if seed%4 == 0 {
			mode = 1
		}
		dir := t.TempDir()
		bp := "func P() int { 1 }\n"
		switch mode {
		case 0:
			bp = "import app/a/q\n\nfunc P() int { q.Q() }\n"
		case 2:
			bp = "import app/a/q\n\nfunc P() int { 1 }\n\ntest Back {\n\tassert q.Q() == 2\n}\n"
		}
		files := map[string]string{
			"ergo.mod":          "module app\n",
			"a/q/q.ergo":        "func Q() int { 2 }\n",
			"b/q/q.ergo":        "func Q() int { 3 }\n",
			"a/p/p.ergo":        "import app/b/q\n\nfunc P() int { q.Q() }\n",
			"b/p/p.ergo":        bp,
			"sub/leaf/l.ergo":   "func L() int { 4 }\n",
			"sub/top.ergo":      "import app/sub/leaf\n\nfunc T() int { leaf.L() }\n",
			"cmd/app/main.ergo": "import app/a/p\nimport bp app/b/p\nimport app/sub\n\nfunc main() needs Console {\n\tConsole.Println(\"\\(p.P() + bp.P() + sub.T())\")\n}\n",
		}
		for name, text := range files {
			path := filepath.Join(dir, filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		a, err := sketchfront.CheckDir(filepath.Join(dir, "cmd", "app"))
		if err != nil {
			t.Fatal(err)
		}
		in := lint.InputOf(a)
		if !in.Checked {
			t.Fatalf("seed %d: does not check: %v", seed, a.Diagnostics)
		}
		replay := fmt.Sprintf("replay: go test -run TestSiblingFolderCycleAgreesWithReference ./internal/sketch/lint -lint.seed=%d", seed)
		res := folderCycleConf(t).Run(in)
		var got []string
		for _, f := range res.Findings {
			got = append(got, f.Message)
		}
		slices.Sort(got)
		var want []string
		if mode == 0 {
			want = append(want, "folders under . have no dependency order: a -> b -> a (2 folders)")
			hits++
		} else {
			clean++
		}
		if !slices.Equal(got, want) {
			t.Fatalf("seed %d (%s) mode=%d:\ngot  %q\nwant %q", seed, replay, mode, got, want)
		}
		if !res.PolicyPassed(true) || lint.GateFails(res.Findings, 0) {
			t.Fatalf("seed %d (%s): advisory finding fails a gate", seed, replay)
		}
	}
	if len(propertySeeds(16)) > 1 && (hits == 0 || clean == 0) {
		t.Fatalf("vacuous: hits=%d clean=%d", hits, clean)
	}
}
