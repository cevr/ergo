package lint_test

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/lint"
	"ergo.local/prototype/internal/sketchfront"
)

func seamConf(t *testing.T, seams []map[string]any) *lint.Configuration {
	t.Helper()
	settings := map[string]lint.Setting{}
	for _, r := range lint.Builtin().Rules() {
		settings[r.Name] = lint.Setting{Severity: lint.Off, HasSeverity: true}
	}
	s := lint.Setting{}
	if len(seams) > 0 {
		s.Options, _ = json.Marshal(map[string]any{"seams": seams})
	}
	settings["seam-entrypoints"] = s
	conf, err := lint.Builtin().Configure(lint.Config{Version: lint.ConfigVersion, Rules: settings}, []string{lint.Layout})
	if err != nil {
		t.Fatal(err)
	}
	return conf
}

// TestSeamEntrypointsAgreesWithReference tests:
// - outside caller importing allowed entry passes;
// - outside caller importing non-entry internal package fails;
// - ordinary in-seam caller passes;
// - mixed in-seam file (ordinary caller + test) fails with testThroughEntries;
// - colocated law in-seam passes with testThroughEntries;
// - outside test-only import of configured testSupport passes;
// - invalid entry outside seam area is refused at Configure.
// Mutant: layout-seam-mixed-file-exempts-test (exempts an import once any
// ordinary caller in the file uses it).
func TestSeamEntrypointsAgreesWithReference(t *testing.T) {
	// Config validation: entry outside seam area must be refused.
	badOpts, _ := json.Marshal(map[string]any{"seams": []map[string]any{{"root": "app/sched", "entries": []string{"app/outside"}}}})
	if _, err := lint.Builtin().Configure(lint.Config{Version: lint.ConfigVersion, Rules: map[string]lint.Setting{"seam-entrypoints": {Options: badOpts}}}, []string{lint.Layout}); err == nil {
		t.Fatal("expected entry outside seam area to be refused")
	}

	outsideHits, insideTestHits, clean := 0, 0, 0
	for _, seed := range propertySeeds(16) {
		r := rand.New(rand.NewPCG(seed, 0x5ea1))
		outsideBypass := r.IntN(2) == 0
		insideTest := r.IntN(2) == 0
		if seed%4 == 0 {
			outsideBypass, insideTest = false, false
		}
		dir := t.TempDir()

		var worker strings.Builder
		worker.WriteString("import app/sched/heap\n\n")
		worker.WriteString("func Step(n int) int { heap.Push(n) }\n\n")
		worker.WriteString("law StepLaw(n int) { heap.Push(n) == n + 1 }\n")
		if insideTest {
			worker.WriteString("\ntest InsideDirect {\n\tassert heap.Push(1) == 2\n}\n")
		}

		var watch strings.Builder
		watch.WriteString("import app/sched\nimport app/sched/fix\n")
		watchCall := "sched.Run(1)"
		if outsideBypass {
			watch.WriteString("import app/sched/heap\n")
			watchCall = "sched.Run(1) + heap.Push(2)"
		}
		watch.WriteString("\nfunc Watch() int { " + watchCall + " }\n\n")
		watch.WriteString("test UsesFix {\n\tassert fix.Stub() == 7\n}\n")

		files := map[string]string{
			"ergo.mod":             "module app\n",
			"sched/heap/heap.ergo": "func Push(n int) int { n + 1 }\n",
			"sched/fix/fix.ergo":   "func Stub() int { 7 }\n",
			"sched/worker/w.ergo":  worker.String(),
			"sched/sched.ergo":     "import app/sched/worker\n\nfunc Run(n int) int { worker.Step(n) }\n",
			"watch/watch.ergo":     watch.String(),
			"cmd/app/main.ergo":    "import app/watch\n\nfunc main() needs Console {\n\tConsole.Println(\"\\(watch.Watch())\")\n}\n",
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
		replay := fmt.Sprintf("replay: go test -run TestSeamEntrypointsAgreesWithReference ./internal/sketch/lint -lint.seed=%d", seed)
		if res := seamConf(t, nil).Run(in); len(res.Findings) != 0 {
			t.Fatalf("seed %d (%s): inactive config reported %v", seed, replay, res.Findings)
		}
		seams := []map[string]any{{
			"root":               "app/sched",
			"entries":            []string{"app/sched"},
			"testSupport":        []string{"app/sched/fix"},
			"testThroughEntries": true,
		}}
		res := seamConf(t, seams).Run(in)
		var got []string
		for _, f := range res.Findings {
			got = append(got, f.File+":"+f.Message)
		}
		slices.Sort(got)
		var want []string
		if outsideBypass {
			want = append(want, "watch/watch.ergo:app/watch enters app/sched at app/sched/heap; enter at app/sched")
			outsideHits++
		}
		if insideTest {
			want = append(want, "sched/worker/w.ergo:test in app/sched/worker enters app/sched at internal package app/sched/heap; enter at app/sched")
			insideTestHits++
		}
		if !outsideBypass && !insideTest {
			clean++
		}
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Fatalf("seed %d (%s):\ngot  %q\nwant %q", seed, replay, got, want)
		}
		if !res.PolicyPassed(true) || lint.GateFails(res.Findings, 0) {
			t.Fatalf("seed %d (%s): advisory finding fails a gate", seed, replay)
		}
	}
	if len(propertySeeds(16)) > 1 && (outsideHits == 0 || insideTestHits == 0 || clean == 0) {
		t.Fatalf("vacuous: outside=%d insideTest=%d clean=%d", outsideHits, insideTestHits, clean)
	}
}
