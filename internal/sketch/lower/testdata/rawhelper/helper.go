// Package rawhelper reads a checked package's raw lists from outside the
// lowering, the round 5 verify's external-helper escape (R5 M1): no guard
// of package lower sees these reads. TestRawTestsReaderIsCanonical and
// TestRawListEscapesAreCanonical use it to show that the lists are
// canonical when the checker hands them out (check/canon.go).
package rawhelper

import "ergo.local/prototype/internal/sketch/check"

func Funcs(p *check.Package) []*check.Func     { return p.Funcs }
func Types(p *check.Package) []*check.TypeName { return p.Types }
func Tests(p *check.Package) []*check.Test     { return p.Tests }
func Laws(p *check.Package) []*check.Law       { return p.Laws }
