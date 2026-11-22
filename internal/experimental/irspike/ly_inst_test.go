package irspike

import "testing"

// TestIRInstancesMatchSource is the seeded property over generic bodies
// run at the program's types (FLIP F7 LY-FIX, GenerateInstances): each
// program builds and prints the same under both lowerings, and the
// functions that call the copies have IR bodies. The monomorphic copies
// (mono.go) have IR bodies too, and the IR calls them, not the generic
// function (MONO-HOOKS-MIN): a copy is compiled once, where a Go instance
// is compiled again in every package that names it. A failing seed prints
// the Ergo source, both outcomes and its replay command.
//
// It builds Go because a copy's body and its function's are two Go
// declarations, and only the Go build of the IR file shows that an IR
// body took the wrong one's name (a461a6b13: "Fold is not declared in
// the Go source"), or that a method's copy reads its environment where
// its receiver is.
//
// flips converts Switch, whose after function gives a machines.Every
// timer, to its Machine: its After is the IR's everyAfter (CENSUS-TAIL-2).
//
// Mutants: mono-ir-calls-stencil, mono-ir-copy-text, mono-ir-copy-method-order,
// every-after-refused.
func TestIRInstancesMatchSource(t *testing.T) {
	lyProperty(t, "TestIRInstancesMatchSource", GenerateInstances, []string{"FoldMono_i", "BoxCountMono_i", "BoxShowMono_i"},
		"tally", "spans", "size", "sized", "flips", "RunSwitch", "stepOnceSwitch", "boxes", "FoldMono_i1", "BoxCountMono_i1", "BoxShowMono_i1")
}
