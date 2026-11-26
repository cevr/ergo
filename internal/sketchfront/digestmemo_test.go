package sketchfront

import (
	"path/filepath"
	"slices"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

// TestDigestMemoAgreesWithDirectWalk (PERF-LOWER-BUILD-1): the keys of a
// package's law runs and tests read through one shared memo
// (lower.Digests, as routes, the evaluator's and the test build's verdict
// views share it) are the keys of the direct walk the memo replaced
// (lower.ReferenceDigests: a memo of its own for each digest and no edge
// replay), whatever order the targets are keyed in, over packages whose
// runs reach contexts, bindings, implicit conversions, qualified lets and
// tests.
//
// Mutants (lower mutants.json): perf-lower-digest-replay-ignores-derived,
// perf-lower-digest-edge-drops-selector,
// perf-lower-digest-member-memo-ignores-name,
// perf-lower-digest-text-memo-ignores-node.
func TestDigestMemoAgreesWithDirectWalk(t *testing.T) {
	var contexts, bindings, tests int
	for _, rel := range []string{
		"modules/git/oid",
		"modules/git/delta",
		"internal/sketch/std/json",
		"internal/sketch/std/queue",
		"internal/sketch/std/fs",
	} {
		dir, err := filepath.Abs(filepath.Join("..", "..", rel))
		if err != nil {
			t.Fatal(err)
		}
		a, err := CheckDir(dir)
		if err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
		pkg := a.rootPackage()
		if pkg == nil {
			t.Fatalf("%s does not check:\n%s", rel, diagnosticLines(a))
		}
		keys := foreignKeys(a, nil)
		targets := lower.RunTargets(pkg)
		want := make([]string, len(targets))
		for i, rt := range targets {
			want[i] = rt.DigestIn(lower.ReferenceDigests(), pkg, keys)
			if rt.Context != nil {
				contexts++
			} else if len(rt.Binding) > 0 {
				bindings++
			}
		}
		wantTests := make([]string, len(pkg.Tests))
		for i, tt := range pkg.Tests {
			wantTests[i] = lower.TestDigestIn(lower.ReferenceDigests(), pkg, tt, keys)
		}
		tests += len(pkg.Tests)
		for _, reverse := range []bool{false, true} {
			memo := lower.NewDigests()
			order := make([]int, len(targets))
			for i := range order {
				order[i] = i
			}
			if reverse {
				slices.Reverse(order)
			}
			testsFirst := reverse
			keyTests := func() {
				for i, tt := range pkg.Tests {
					if got := lower.TestDigestIn(memo, pkg, tt, keys); got != wantTests[i] {
						t.Errorf("%s: test %s (reverse %t): memo digest %s, direct walk %s", rel, tt.Name, reverse, got, wantTests[i])
					}
				}
			}
			if testsFirst {
				keyTests()
			}
			for _, i := range order {
				if got := targets[i].DigestIn(memo, pkg, keys); got != want[i] {
					t.Errorf("%s: %s (reverse %t): memo digest %s, direct walk %s", rel, targets[i].Label, reverse, got, want[i])
				}
			}
			if !testsFirst {
				keyTests()
			}
		}
	}
	if contexts == 0 || bindings == 0 || tests == 0 {
		t.Fatalf("the corpus keys %d runs under a context, %d bindings and %d tests; the property needs each", contexts, bindings, tests)
	}
	t.Logf("keyed %d runs under a context, %d bindings, %d tests", contexts, bindings, tests)
}
