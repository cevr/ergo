package lint

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// derivedField is the strict hint for a stored value the other fields
// already decide (SKILL-RULES SR-ERGO-1; React's "derived state"): a
// record field (a machine's state is a record) that every construction,
// update and assignment sets to one form over the record's other fields:
// the same tree of constants, operators and calls of pure value functions
// (check.PureValueFunc) over the siblings' values. A construction that
// leaves the field out gives it the zero value; an update that sets a
// sibling the form reads and not the field breaks it: neither is a
// derived field. An exported record counts only in a program with main,
// where every write is in the run.
var derivedField = &Rule{
	Name:    "derived-field",
	Code:    "L149",
	Doc:     "A field every write sets to the same pure expression of the other fields stores what it can compute, and can fall out of step with them.",
	Instead: "drop the field and compute it from the others with a function where it is read",
	Default: Hint,
	Check:   checkDerivedField,
}

func checkDerivedField(p *Pass) {
	ff := p.Memo(func() any { return collectFieldForms(p.Program(), p.run.shared) }).(*fieldForms)
	for _, d := range p.File.Decls {
		td, ok := d.(*syntax.TypeDecl)
		if !ok {
			continue
		}
		tn, ok := p.Info.Defs[td.Name].(*check.TypeName)
		if !ok || exportedName(td.Name) && !p.Closed() {
			continue
		}
		for _, f := range recordFields(tn.Type) {
			if ff.derived(f) {
				p.Report(f.Pos, f.Pos+syntax.Pos(len(f.Name)), f.Name+" is set to the same expression of the other fields at every write")
			}
		}
	}
}
