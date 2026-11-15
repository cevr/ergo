package sketchfront

import (
	"errors"

	"ergo.local/prototype/internal/sketch/lower"
	"ergo.local/prototype/internal/sketch/syntax"
)

// IRSpikeUnit is the lowering unit Build would lower for a, whose single
// source file (when a is not a folder program) is admitted as source.
// EXPERIMENTAL (wf10 IR1 spike, internal/experimental/irspike): the spike's
// driver lowers it with both lower.Lower and lower.LowerIR. Build never
// calls it.
func IRSpikeUnit(a Analysis, source string) (lower.Unit, error) {
	if len(a.Diagnostics) > 0 || (a.file == nil && a.program == nil) || a.pkg == nil {
		return lower.Unit{}, errors.New("sketchfront: the program does not check")
	}
	unit := lower.Unit{Package: a.pkg, Files: []*syntax.File{a.file}, Sources: []lower.Source{{Path: source, Text: a.source}}}
	if a.program != nil {
		unit = a.program.unit()
	}
	return unit, unit.Validate()
}
