package lint_test

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"

	"ergo.local/prototype/internal/sketch/lint"
)

// TestDependencyUnitsAreNotLinted is the module scope of a run (LH2a, in
// place of the rules' std-membership skips): a package of a required
// module is read, but no rule runs over its files and no directive in it
// is judged. Units of a seeded program, each with or without a finding
// and a directive that covers no finding, are marked dependencies at
// random; the run's findings are exactly the linted units' own, by the
// one-unit runs of each. Mutants: lh2a-dependency-linted,
// lh2a-dependency-directives-judged.
func TestDependencyUnitsAreNotLinted(t *testing.T) {
	settings := map[string]lint.Setting{}
	for _, r := range lint.Builtin().Rules() {
		settings[r.Name] = lint.Setting{Severity: lint.Off, HasSeverity: true}
	}
	settings["identical-arms"] = lint.Setting{Severity: lint.Warning, HasSeverity: true}
	conf, err := lint.Builtin().Configure(lint.Config{Version: lint.ConfigVersion, Rules: settings}, nil)
	if err != nil {
		t.Fatal(err)
	}
	key := func(f lint.Finding) string { return fmt.Sprintf("%s:%d %s", f.File, f.Line, f.Code) }
	for _, seed := range propertySeeds(20) {
		r := rand.New(rand.NewPCG(seed, 0x5c0))
		in := lint.Input{Checked: true}
		var want []string
		for i := range 2 + r.IntN(4) {
			src := "func pick(k int) int {\n\tmatch k {\n\tcase 1:\n\t\t0\n"
			if r.IntN(2) == 0 {
				src += "\tcase 2:\n\t\t0\n"
			} else {
				src += "\tcase 2:\n\t\t1\n"
			}
			if r.IntN(2) == 0 {
				src += "\t// " + lint.DirectiveWord + " identical-arms -- covers nothing\n"
			}
			src += "\tcase _:\n\t\t2\n\t}\n}\n\nfunc main() {\n}\n"
			one := lintInput(src)
			if !one.Checked {
				t.Fatalf("does not check:\n%s", src)
			}
			unit := one.Units[0]
			for j := range unit.Files {
				unit.Files[j].Rel = fmt.Sprintf("u%d/main.ergo", i)
			}
			unit.Dependency = r.IntN(2) == 0
			in.Units = append(in.Units, unit)
			if !unit.Dependency {
				for _, f := range conf.Run(lint.Input{Checked: true, Units: []lint.Unit{unit}}).Findings {
					want = append(want, key(f))
				}
			}
		}
		var got []string
		for _, f := range conf.Run(in).Findings {
			got = append(got, key(f))
		}
		slices.Sort(got)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Fatalf("seed %d: findings %v, the linted units' own are %v\nreplay: go test ./internal/sketch/lint -run TestDependencyUnitsAreNotLinted -lint.seed=%d", seed, got, want, seed)
		}
	}
}
