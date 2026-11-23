package irspike

import (
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

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
// Mutants: mono-ir-calls-stencil, mono-ir-copy-text, mono-ir-copy-method-order.
func TestIRInstancesMatchSource(t *testing.T) {
	// Copies are declarations in their own package. Match the instance
	// the IR calls, not a particular number in the instance's name.
	copies := []string{lower.PackageAlias("slices") + "__FoldMono_i", "BoxCountMono_i", "BoxShowMono_i"}
	lyProperty(t, "TestIRInstancesMatchSource", GenerateInstances, copies,
		append([]string{"tally", "spans", "size", "sized", lower.HelperSpelling("", "RunSwitch"), lower.HelperSpelling("", "stepOnceSwitch"), "boxes"}, copies...)...)
}
