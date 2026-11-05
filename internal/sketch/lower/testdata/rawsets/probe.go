// Package rawsets is the round 7 fixture of TestLoweringReadsCanonicalLists:
// each function reads a set of the check result in the checker's intern
// order (check/canon.go, kind b), as lower read main's row for its
// refusal before round 7 (R6 M1). The guard must report each of them.
package rawsets

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/lower"
)

// The guard resolves Unit's fields too, so the fixture imports lower.
var _ = lower.ValidatePath

// caught: a row's elements.
func escRow(s *check.Signature) check.Type { return s.Uses.Elems()[0] }

// caught: a row value's variants.
func escRowValue(u *check.Union) int { return len(u.Variants) }

// caught: a catch's subjects.
func escCatch(m *check.Match) []check.Type { return m.Subjects }

// caught: the capabilities an entry's provider uses.
func escEntry(e *check.ContextEntry) []check.Type { return e.Uses }

// allowed: the canonical views.
func views(s *check.Signature, u *check.Union, m *check.Match, e *check.ContextEntry) int {
	return len(check.CanonElems(s.Uses)) + len(check.CanonVariants(u)) + len(check.CanonCatchSubjects(m)) + len(check.CanonEntryUses(e))
}
