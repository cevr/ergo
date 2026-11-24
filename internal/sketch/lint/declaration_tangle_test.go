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

func tangleConf(t *testing.T, max int) *lint.Configuration {
	t.Helper()
	settings := map[string]lint.Setting{}
	for _, r := range lint.Builtin().Rules() {
		settings[r.Name] = lint.Setting{Severity: lint.Off, HasSeverity: true}
	}
	s := lint.Setting{}
	if max > 0 {
		s.Options, _ = json.Marshal(map[string]int{"maxTangleMembers": max})
	}
	settings["declaration-tangle"] = s
	conf, err := lint.Builtin().Configure(lint.Config{Version: lint.ConfigVersion, Rules: settings}, []string{lint.Layout})
	if err != nil {
		t.Fatal(err)
	}
	return conf
}

// TestDeclarationTangleAgreesWithReference exercises:
// - mutually recursive declarations split across two files (reported with no max);
// - mutually recursive declarations in one file (reported only when maxTangleMembers exceeded);
// - disjoint one-way declaration edges forming a file-level cycle (NOT a declaration SCC);
// - a law referencing a type across files (test/law filtered out).
// Mutant: layout-tangle-file-instead-of-decl-scc (projects onto file nodes
// instead of top-level declarations, falsely reporting the disjoint edges).
func TestDeclarationTangleAgreesWithReference(t *testing.T) {
	crossHits, sizeHits, clean := 0, 0, 0
	for _, seed := range propertySeeds(16) {
		r := rand.New(rand.NewPCG(seed, 0xda1e))
		splitCycle := r.IntN(2) == 0
		sameFileCycle := r.IntN(2) == 0
		if seed%4 == 0 {
			splitCycle, sameFileCycle = false, false
		}
		dir := t.TempDir()

		// Package `disjoint`: file-level cycle with NO declaration SCC, plus a law control.
		disjointA := strings.Join([]string{
			"type Box { V int }",
			"func A1(n int) int { B1(n) }",
			"func A2(n int) int { n + 1 }",
		}, "\n") + "\n"
		disjointB := strings.Join([]string{
			"func B1(n int) int { n + 2 }",
			"func B2(n int) int { A2(n) }",
			"law BoxLaw(n int) { Box{V: n}.V == n }",
		}, "\n") + "\n"

		// Package `parse`: optional cross-file mutual recursion (via a re-exporting let alias)
		// and optional same-file 3-member mutual recursion.
		parseA := "func Start(n int) int { n }\n"
		parseB := "func Helper(n int) int { n + 1 }\n"
		if splitCycle {
			parseA = "let nextStep = Node\n\nfunc Parse(n int) int {\n\tif n <= 0 {\n\t\treturn 0\n\t}\n\tnextStep(n - 1)\n}\n"
			parseB = "func Node(n int) int {\n\tif n <= 0 {\n\t\treturn 1\n\t}\n\tParse(n - 1)\n}\n"
		}
		sameFile := "func Together(n int) int { n }\n"
		if sameFileCycle {
			sameFile = strings.Join([]string{
				"func S1(n int) int { if n <= 0 { return 0 }; S2(n - 1) }",
				"func S2(n int) int { if n <= 0 { return 1 }; S3(n - 1) }",
				"func S3(n int) int { if n <= 0 { return 2 }; S1(n - 1) }",
				"func Together(n int) int { S1(n) }",
			}, "\n") + "\n"
		}
		mainCall := "disjoint.A1(1) + disjoint.B2(2) + parse.Together(3)"
		if splitCycle {
			mainCall += " + parse.Parse(4)"
		} else {
			mainCall += " + parse.Start(4) + parse.Helper(5)"
		}

		files := map[string]string{
			"ergo.mod":            "module app\n",
			"disjoint/a.ergo":     disjointA,
			"disjoint/b.ergo":     disjointB,
			"parse/a.ergo":        parseA,
			"parse/b.ergo":        parseB,
			"parse/together.ergo": sameFile,
			"cmd/app/main.ergo":   "import app/disjoint\nimport app/parse\n\nfunc main() needs Console {\n\tConsole.Println(\"\\(" + mainCall + ")\")\n}\n",
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
		replay := fmt.Sprintf("replay: go test -run TestDeclarationTangleAgreesWithReference ./internal/sketch/lint -lint.seed=%d", seed)

		// Without maxTangleMembers: ONLY cross-file declaration SCCs report (never same-file, never disjoint file cycle).
		res0 := tangleConf(t, 0).Run(in)
		var got0 []string
		for _, f := range res0.Findings {
			got0 = append(got0, f.Message)
		}
		slices.Sort(got0)
		var want0 []string
		if splitCycle {
			want0 = append(want0, "2 declarations in app/parse are mutually dependent across 2 files: app/parse.Parse -> app/parse.Node -> app/parse.Parse")
			crossHits++
		} else {
			clean++
		}
		if !slices.Equal(got0, want0) {
			t.Fatalf("seed %d (%s) no-max:\ngot  %q\nwant %q", seed, replay, got0, want0)
		}
		if !res0.PolicyPassed(true) || lint.GateFails(res0.Findings, 0) {
			t.Fatalf("seed %d (%s): advisory finding fails a gate", seed, replay)
		}

		// With maxTangleMembers=2: same-file 3-member SCC also reports.
		res2 := tangleConf(t, 2).Run(in)
		var got2 []string
		for _, f := range res2.Findings {
			got2 = append(got2, f.Message)
		}
		slices.Sort(got2)
		want2 := slices.Clone(want0)
		if sameFileCycle {
			want2 = append(want2, "3 declarations in app/parse are mutually dependent (configured tangle max: 2): app/parse.S1 -> app/parse.S2 -> app/parse.S3 -> app/parse.S1")
			sizeHits++
		}
		slices.Sort(want2)
		if !slices.Equal(got2, want2) {
			t.Fatalf("seed %d (%s) max=2:\ngot  %q\nwant %q", seed, replay, got2, want2)
		}
	}
	if len(propertySeeds(16)) > 1 && (crossHits == 0 || sizeHits == 0 || clean == 0) {
		t.Fatalf("vacuous: cross=%d size=%d clean=%d", crossHits, sizeHits, clean)
	}
}
