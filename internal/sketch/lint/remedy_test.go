package lint_test

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/report"
	"ergo.local/prototype/internal/sketch/lint"
	"ergo.local/prototype/internal/sketchfront"
)

// TestRegistryRefusesRuleWithoutRemedy is the diagnostic row of AS1: a
// rule with no Instead is refused, by the error's text. Mutant:
// as1-empty-remedy-admitted.
func TestRegistryRefusesRuleWithoutRemedy(t *testing.T) {
	rule := &lint.Rule{Name: "no-remedy", Code: "L901", Doc: "d", Default: lint.Warning, Check: func(*lint.Pass) {}}
	_, err := lint.NewRegistry([]*lint.Rule{rule}, []lint.Preset{{Name: "base", Rules: map[string]lint.Setting{}}}, "base")
	want := "rule no-remedy has no remedy: write Instead, what to do in place of what it reports"
	if err == nil || err.Error() != want {
		t.Fatalf("got %v, want %q", err, want)
	}
}

// TestFindingsRenderTheirRemedy is AS1's property: over the planted
// program, generated functions and a seeded draw of the examples'
// packages, with every built-in rule on, each finding of a built-in rule
// carries a remedy, and the text report prints its line as the message,
// "; " and the remedy; the LSP message is the same text. Mutants:
// as1-renderer-drops-remedy, as1-rule-remedy-unset.
func TestFindingsRenderTheirRemedy(t *testing.T) {
	conf := allRules(t)
	var targets []target
	for _, seed := range propertySeeds(4) {
		r := rand.New(rand.NewPCG(seed, 0xa5))
		var src strings.Builder
		for i := 0; i < 1+r.IntN(3); i++ {
			src.WriteString(generateFunc(r, fmt.Sprintf("g%d", i)).text + "\n")
		}
		src.WriteString("func main() {\n}\n")
		targets = append(targets, target{name: fmt.Sprintf("generated seed %d", seed), src: src.String()})
	}
	targets = append(targets, target{name: "planted", src: plantedForTransforms})
	mods, err := filepath.Glob(filepath.Join("..", "..", "..", "examples", "*", "ergo.mod"))
	if err != nil || len(mods) == 0 {
		t.Fatalf("no example modules: %v", err)
	}
	r := rand.New(rand.NewPCG(*seedFlag+1, 0xa1))
	r.Shuffle(len(mods), func(i, j int) { mods[i], mods[j] = mods[j], mods[i] })
	for _, mod := range mods[:6] {
		root, err := filepath.Abs(filepath.Dir(mod))
		if err != nil {
			t.Fatal(err)
		}
		pkgs, err := sketchfront.DiscoverPackages(root)
		if err != nil || len(pkgs) == 0 {
			t.Fatalf("%s: %v", root, err)
		}
		p := pkgs[r.IntN(len(pkgs))]
		targets = append(targets, target{name: p.Dir, dir: p.Dir, root: root})
	}
	rules := map[string]bool{}
	seen := 0
	for _, tg := range targets {
		in, ok := tg.check(t, nil)
		if !ok {
			continue
		}
		res := conf.Run(in)
		snapshot := report.SourceSnapshot{URI: "file:///m", Files: map[string]string{}}
		for _, u := range in.Units {
			for _, f := range u.Files {
				if f.Syntax != nil {
					snapshot.Files[f.Rel] = f.Syntax.Src
				}
			}
		}
		rep := report.NewResult("", report.Target, nil).ReportWithLint(snapshot, false, res.Advice())
		var text bytes.Buffer
		if err := rep.WriteText(&text, "m"); err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(text.String(), "\n")
		for i, d := range rep.Diagnostics {
			if d.Origin != "lint" || d.Rule == "" || strings.Contains(d.Rule, "/") {
				continue
			}
			seen++
			rules[d.Rule] = true
			if d.Instead == "" {
				t.Errorf("%s: a %s finding has no remedy: %q", tg.name, d.Rule, d.Message)
				continue
			}
			want := d.Message + "; " + d.Instead
			if !strings.Contains(lines[i], ": "+want) || d.LSP.Message != want {
				t.Errorf("%s: %s renders %q (LSP %q), want the message, \"; \" and the remedy: %q", tg.name, d.Rule, lines[i], d.LSP.Message, want)
			}
		}
	}
	if seen == 0 {
		t.Fatal("no built-in rule reported a finding")
	}
	t.Logf("findings: %d, of rules %d", seen, len(rules))
}
