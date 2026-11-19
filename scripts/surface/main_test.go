package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/testinputs"
)

// TestMain runs the tests under observation, so the files and variables
// any process they start reads are inputs of Go's test cache.
func TestMain(m *testing.M) { os.Exit(testinputs.Main(m)) }

// TestREADMESurfaceIsCurrent is the acceptance check: the surface block
// of docs/spec/README.md holds the count of the parser and the spec at
// this head. Mutants: surface-new-word (a contextual word the parser
// reads with no README row), surface-new-production (a grammar
// production and clause with none).
func TestREADMESurfaceIsCurrent(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	if err := run(root, filepath.Join(root, "docs/spec/README.md"), ""); err != nil {
		t.Fatal(err)
	}
}

// TestProbationNeedsTwoCounts: a built form goes to the owner when it is
// under the probation count now and at the README's previous count, not
// at one low count; a planned form never does. Mutant:
// surface-probation-one-count.
func TestProbationNeedsTwoCounts(t *testing.T) {
	s := &Surface{Forms: []Form{
		{Name: "rare", Kind: kindWord, Uses: 3},
		{Name: "fresh", Kind: kindWord, Uses: 3},
		{Name: "risen", Kind: kindWord, Uses: 3},
		{Name: "later", Kind: kindPlanned},
	}}
	prev := []Form{
		{Name: "rare", Kind: kindWord, Uses: 4},
		{Name: "risen", Kind: kindWord, Uses: 40},
		{Name: "later", Kind: kindPlanned},
	}
	text := render(s, prev)
	owner := map[string]bool{}
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, "delete or teach") && strings.HasPrefix(line, "| `") {
			owner[strings.Trim(strings.Fields(line)[1], "`")] = true
		}
	}
	if !owner["rare"] || owner["fresh"] || owner["risen"] || owner["later"] || len(owner) != 1 {
		t.Fatalf("for the owner: %v, want only rare\n%s", owner, text)
	}
}
