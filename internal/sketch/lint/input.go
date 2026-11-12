package lint

import "ergo.local/prototype/internal/sketchfront"

// InputOf is the program an analysis checked, as lint reads it: each
// package's checked facts and parsed files, dependencies first. It is
// checked when the analysis has no diagnostic and every package checked.
func InputOf(a sketchfront.Analysis) Input {
	in := Input{Checked: len(a.Diagnostics) == 0 && a.Checked() != nil}
	packages := a.Packages()
	if packages == nil {
		packages = append(packages, a.Root())
	}
	for _, p := range packages {
		unit := Unit{Package: p.Package}
		for _, f := range p.Files {
			unit.Files = append(unit.Files, File{Rel: f.Rel, Syntax: f.Syntax})
		}
		in.Units = append(in.Units, unit)
	}
	return in
}
