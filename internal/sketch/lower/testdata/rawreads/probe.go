// Package rawreads is the fixture of TestLoweringReadsCanonicalLists: each
// function marked "caught" reads a raw list by a spelling that is not the
// check or lower type, so the guard must report it. The round 4 verify's
// escape probe (R4 M1) is cases 0 to 5.
package rawreads

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/lower"
)

// 0. caught: a direct read (the control).
func escDirect(p *check.Package) []*check.Law { return p.Laws }

// 1. caught: a field promoted through an embedded *check.Package.
type escEmbed struct{ *check.Package }

func escPromoted(p *check.Package) []*check.Test { return escEmbed{p}.Tests }

// 2. caught: a type alias of check.Package.
type escAlias = check.Package

func escViaAlias(p *escAlias) []*check.Func { return p.Funcs }

// 3. caught: a defined type over check.Package, reached by conversion.
type escDefined check.Package

func escViaDefined(p *check.Package) []*check.TypeName { return (*escDefined)(p).Types }

// 4. caught: a local copy type of Unit, reached by conversion.
func escViaMirror(u lower.Unit) int {
	type mirror lower.Unit
	return len(mirror(u).Files)
}

// 5. caught: a field promoted through an embedded *check.Info.
type escInfo struct{ *check.Info }

func escInitOrder(p *check.Package) int { return len(escInfo{p.Info}.InitOrder) }
