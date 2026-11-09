// Package rawunits is the fixture of TestLoweringUnitStaysAtEntries: each
// declaration marked "caught" holds a value of a Unit type outside the
// entry functions, where the caller's order of files and imports could be
// read by a route that names no list field: the round 5 verify's anonymous
// struct conversion (R5 M1), a helper given the Unit, a field that keeps
// it past the entry, and a list of Units.
package rawunits

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/lower"
	"ergo.local/prototype/internal/sketch/syntax"
)

// caught: a conversion to an anonymous struct of the same fields.
func escConvert(u lower.Unit) []*syntax.File {
	return (struct {
		Package *check.Package
		Files   []*syntax.File
		Sources []lower.Source
		Imports []lower.Unit
		Go      lower.GoModule
	})(u).Files
}

// caught: a helper that is given the Unit.
func escHelper(u lower.Unit) int { return count(&u) }

// caught: the helper itself.
func count(u *lower.Unit) int { return len(u.Files) }

// caught: a field that keeps the Unit past the entry.
type escHolder struct{ unit lower.Unit }

// caught: a list of Units.
func escImports(us []lower.Unit) int { return len(us) }
