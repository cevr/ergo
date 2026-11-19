package lint

import "ergo.local/prototype/internal/sketch/stdlib"

// foreignWhereStd is the justifier rule for raw Go where std has the
// package (plan §3; tsgo nodeBuiltinImport and preferSchemaOverJson,
// effect-oxlint noNodeBuiltinImport). The coverage table is std's
// (std.CoveredBy); the import's identity is the host package it resolved
// to, never the spec's text.
var foreignWhereStd = &Rule{
	Name:    "foreign-where-std",
	Code:    "L103",
	Doc:     "An import go of a Go package that a std package covers runs unreviewed Go (go.Foreign) where std has typed failures and capabilities; import the std package.",
	Instead: "import the std package that covers it (docs/spec/stdlib.md)",
	Default: Warning,
	Check:   checkForeignWhereStd,
}

func checkForeignWhereStd(p *Pass) {
	for _, im := range p.Package.ImportsOf(p.File) {
		if !im.Pkg.Host() {
			continue
		}
		if covering, ok := stdlib.CoveredBy(im.Pkg.Path); ok {
			p.ReportInstead(im.Spec.Pos(), im.Spec.End(), "import go "+im.Pkg.Path+" runs unreviewed Go where std covers it", "import "+covering+" (docs/spec/stdlib.md)", nil)
		}
	}
}
