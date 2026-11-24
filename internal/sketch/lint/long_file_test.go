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

func longFileConf(t *testing.T, max int) *lint.Configuration {
	t.Helper()
	settings := map[string]lint.Setting{}
	for _, r := range lint.Builtin().Rules() {
		settings[r.Name] = lint.Setting{Severity: lint.Off, HasSeverity: true}
	}
	s := lint.Setting{}
	if max > 0 {
		s.Options, _ = json.Marshal(map[string]int{"maxFileLines": max})
	}
	settings["long-file"] = s
	conf, err := lint.Builtin().Configure(lint.Config{Version: lint.ConfigVersion, Rules: settings}, []string{lint.Layout})
	if err != nil {
		t.Fatal(err)
	}
	return conf
}

func physicalLineCount(src string) int {
	if len(src) == 0 {
		return 0
	}
	n := strings.Count(src, "\n")
	if src[len(src)-1] != '\n' {
		n++
	}
	return n
}

// TestLongFileAgreesWithReference generates modules with files spanning
// empty source, exact budget vs budget+1, trailing newline vs no trailing
// newline, and trailing blank lines and comments after the last
// declaration. A direct reference counts physical lines from the written
// bytes. Mutant: layout-long-uses-last-site-line (uses the last
// declaration's end line instead of the physical line count).
func TestLongFileAgreesWithReference(t *testing.T) {
	hits, tailHits, exacts, empties := 0, 0, 0, 0
	for _, seed := range propertySeeds(16) {
		r := rand.New(rand.NewPCG(seed, 0x10f1))
		limit := 3 + r.IntN(6)
		dir := t.TempDir()
		files := map[string]string{
			"ergo.mod":          "module app\n",
			"cmd/app/main.ergo": "import app/pkg\n\nfunc main() needs Console {\n\tConsole.Println(\"\\(pkg.F())\")\n}\n",
			"pkg/empty.ergo":    "",
		}
		empties++
		// File 1: exact budget (with or without trailing newline).
		var exact strings.Builder
		exact.WriteString("func F() int { 1 }")
		for i := 1; i < limit; i++ {
			exact.WriteString("\n// pad")
		}
		if r.IntN(2) == 0 {
			exact.WriteByte('\n')
		}
		files["pkg/exact.ergo"] = exact.String()
		exacts++

		// File 2: last declaration is line 1 (<= limit), but trailing blank
		// lines and comments push physical line count over limit.
		var tail strings.Builder
		tail.WriteString("func G() int { 2 }")
		extra := 1 + r.IntN(4)
		for i := 1; i < limit+extra; i++ {
			if i%2 == 0 {
				tail.WriteString("\n")
			} else {
				tail.WriteString("\n// tail comment")
			}
		}
		if r.IntN(2) == 0 {
			tail.WriteByte('\n')
		}
		files["pkg/tail.ergo"] = tail.String()

		// File 3: drawn ordinary declarations, sometimes under, sometimes over.
		var body strings.Builder
		decls := 1 + r.IntN(limit+2)
		for i := range decls {
			fmt.Fprintf(&body, "func H%d() int { %d }\n", i, i)
		}
		files["pkg/body.ergo"] = body.String()

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
		replay := fmt.Sprintf("replay: go test -run TestLongFileAgreesWithReference ./internal/sketch/lint -lint.seed=%d", seed)
		if res := longFileConf(t, 0).Run(in); len(res.Findings) != 0 || !res.PolicyPassed(true) {
			t.Fatalf("seed %d (%s): option absent reported %v", seed, replay, res.Findings)
		}
		res := longFileConf(t, limit).Run(in)
		var got []string
		for _, f := range res.Findings {
			got = append(got, f.File+":"+f.Message)
			if f.File == "pkg/tail.ergo" {
				tailHits++
			}
			hits++
		}
		slices.Sort(got)
		var want []string
		for name, text := range files {
			if !strings.HasSuffix(name, ".ergo") {
				continue
			}
			if n := physicalLineCount(text); n > limit {
				want = append(want, fmt.Sprintf("%s:%s has %d lines (configured file max: %d)", name, name, n, limit))
			}
		}
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Fatalf("seed %d (%s): max %d:\ngot  %q\nwant %q", seed, replay, limit, got, want)
		}
		if !res.PolicyPassed(true) || lint.GateFails(res.Findings, 0) {
			t.Fatalf("seed %d (%s): advisory finding fails a gate", seed, replay)
		}
	}
	if len(propertySeeds(16)) > 1 && (hits == 0 || tailHits == 0 || exacts == 0 || empties == 0) {
		t.Fatalf("vacuous: hits=%d tailHits=%d exacts=%d empties=%d", hits, tailHits, exacts, empties)
	}
}
