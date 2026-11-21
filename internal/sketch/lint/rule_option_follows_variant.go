package lint

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// optionFollowsVariant is the strict hint for a mode spread over a union
// field and optional fields beside it (SKILL-RULES SR-ERGO-2, SR-E10: a
// mode is a tagged union, not a status with optional fields): a record
// field of a two-variant union with one field-less variant (an Option)
// that every write sets to its present variant only when a sibling union
// field is one variant, and some write gives that sibling another. The
// value belongs in that variant. Out: a field some write sets to a value
// whose variant is not visible (a call's result), and a pair an update or
// assignment changes one at a time. An exported record counts only in a
// program with main.
var optionFollowsVariant = &Rule{
	Name:    "option-fields-follow-variant",
	Code:    "L150",
	Doc:     "An optional field present only under one variant of a sibling union field is part of that variant, kept beside it where every reader must check both.",
	Instead: "move the field into that variant's record and drop the Option",
	Default: Hint,
	Check:   checkOptionFollowsVariant,
}

func checkOptionFollowsVariant(p *Pass) {
	vw := p.Memo(func() any { return collectVariantWrites(p.Program()) }).(*variantWrites)
	for _, d := range p.File.Decls {
		td, ok := d.(*syntax.TypeDecl)
		if !ok || exportedName(td.Name) && !p.Closed() {
			continue
		}
		rec, ok := p.Info.Defs[td.Name].(*check.TypeName)
		if !ok {
			continue
		}
		fields := recordFields(rec.Type)
		for _, opt := range fields {
			if !optionLike(unionOf(opt.Type)) {
				continue
			}
			for _, driver := range fields {
				if driver == opt || unionOf(driver.Type) == nil {
					continue
				}
				if under := vw.follows(rec, opt, driver); under != nil {
					p.Report(opt.Pos, opt.Pos+syntax.Pos(len(opt.Name)), opt.Name+" is set only when "+driver.Name+" is "+under.Name)
					break
				}
			}
		}
	}
}
