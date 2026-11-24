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

func wideDirConf(t *testing.T, max int) *lint.Configuration {
	t.Helper()
	settings := map[string]lint.Setting{}
	for _, r := range lint.Builtin().Rules() {
		settings[r.Name] = lint.Setting{Severity: lint.Off, HasSeverity: true}
	}
	s := lint.Setting{}
	if max > 0 {
		s.Options, _ = json.Marshal(map[string]int{"maxDirectoryMembers": max})
	}
	settings["wide-directory"] = s
	conf, err := lint.Builtin().Configure(lint.Config{Version: lint.ConfigVersion, Rules: settings}, []string{lint.Layout})
	if err != nil {
		t.Fatal(err)
	}
	return conf
}

func referenceWideDirs(files map[string]string, limit int) []string {
	holders := map[string]map[string]bool{}
	for name := range files {
		if !strings.HasSuffix(name, ".ergo") {
			continue
		}
		parts := strings.Split(name, "/")
		for k, part := range parts {
			dir := "."
			if k > 0 {
				dir = strings.Join(parts[:k], "/")
			}
			if holders[dir] == nil {
				holders[dir] = map[string]bool{}
			}
			holders[dir][part] = true
		}
	}
	var out []string
	for dir, set := range holders {
		if len(set) <= limit {
			continue
		}
		var members []string
		for m := range set {
			members = append(members, m)
		}
		slices.Sort(members)
		preview := strings.Join(members, ", ")
		if len(members) > 5 {
			preview = fmt.Sprintf("%s, and %d more", strings.Join(members[:5], ", "), len(members)-5)
		}
		out = append(out, fmt.Sprintf("%s/ holds %d source entries (configured directory max: %d): %s", dir, len(members), limit, preview))
	}
	slices.Sort(out)
	return out
}

// TestWideDirectoryAgreesWithReference generates modules with flat and
// nested directories, including a parent directory whose immediate
// children stay within budget while its total grandchildren exceed it
// (killing mutant layout-wide-counts-all-descendants), and checks exact
// agreement with a direct reference.
func TestWideDirectoryAgreesWithReference(t *testing.T) {
	wides, nestedSafe := 0, 0
	for _, seed := range propertySeeds(16) {
		r := rand.New(rand.NewPCG(seed, 0x2d17))
		limit := 3 + r.IntN(3) // 3..5
		dir := t.TempDir()
		files := map[string]string{
			"ergo.mod": "module app\n",
		}
		var imports []string
		var calls []string

		// Nested parent `group/` has only 2 child directories (`a/`, `b/`),
		// each with `limit` files (total 2*limit > limit grandchildren,
		// while `group/` itself has only 2 immediate members <= limit).
		for _, sub := range []string{"a", "b"} {
			imports = append(imports, "import app/group/"+sub)
			var sum []string
			for i := range limit {
				files[fmt.Sprintf("group/%s/f%d.ergo", sub, i)] = fmt.Sprintf("func F%d() int { %d }\n", i, i)
				sum = append(sum, fmt.Sprintf("F%d()", i))
			}
			files[fmt.Sprintf("group/%s/f0.ergo", sub)] += fmt.Sprintf("\nfunc Sum() int { %s }\n", strings.Join(sum, " + "))
			calls = append(calls, sub+".Sum()")
		}
		nestedSafe++

		// Flat package `flat/` with drawn number of files (limit-1 .. limit+2).
		nflat := limit - 1 + r.IntN(4)
		imports = append(imports, "import app/flat")
		var flatSum []string
		for i := range nflat {
			files[fmt.Sprintf("flat/p%d.ergo", i)] = fmt.Sprintf("func P%d() int { %d }\n", i, i)
			flatSum = append(flatSum, fmt.Sprintf("P%d()", i))
		}
		files["flat/p0.ergo"] += fmt.Sprintf("\nfunc Sum() int { %s }\n", strings.Join(flatSum, " + "))
		calls = append(calls, "flat.Sum()")

		files["cmd/app/main.ergo"] = fmt.Sprintf("%s\n\nfunc main() needs Console {\n\tConsole.Println(\"\\(%s)\")\n}\n", strings.Join(imports, "\n"), strings.Join(calls, " + "))

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
		replay := fmt.Sprintf("replay: go test -run TestWideDirectoryAgreesWithReference ./internal/sketch/lint -lint.seed=%d", seed)
		if res := wideDirConf(t, 0).Run(in); len(res.Findings) != 0 || !res.PolicyPassed(true) {
			t.Fatalf("seed %d (%s): option absent reported %v", seed, replay, res.Findings)
		}
		res := wideDirConf(t, limit).Run(in)
		var got []string
		for _, f := range res.Findings {
			got = append(got, f.Message)
		}
		slices.Sort(got)
		want := referenceWideDirs(files, limit)
		if !slices.Equal(got, want) {
			t.Fatalf("seed %d (%s): max %d:\ngot  %q\nwant %q", seed, replay, limit, got, want)
		}
		if len(got) > 0 {
			wides++
		}
		if !res.PolicyPassed(true) || lint.GateFails(res.Findings, 0) {
			t.Fatalf("seed %d (%s): advisory finding fails a gate", seed, replay)
		}
	}
	if len(propertySeeds(16)) > 1 && (wides == 0 || nestedSafe == 0) {
		t.Fatalf("vacuous: wides=%d nestedSafe=%d", wides, nestedSafe)
	}
}
