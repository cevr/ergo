package lint

import (
	"cmp"
	"slices"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// duplicateTest is the duplicate-test rule of the strict preset (catalog
// CR25): a test or a top-level law of a package that does what another
// one of the package does: the same parameters and body up to the names
// of their own locals, by shapeOf. Of each group the one first by file
// and name is kept; each other is reported, naming it. A copy adds a run
// and proves nothing new.
var duplicateTest = &Rule{
	Name:    "duplicate-test",
	Code:    "L117",
	Doc:     "A test or law whose parameters and body are another's in its package proves nothing new; delete one.",
	Instead: "delete one",
	Default: Warning,
	Check:   checkDuplicateTest,
}

// testDecl is one test or law of a package: its file, name and shape.
type testDecl struct {
	rel   string
	name  *syntax.Ident
	shape string
}

func checkDuplicateTest(p *Pass) {
	copies := p.Memo(func() any { return map[*check.Package]map[*syntax.Ident]testDecl{} }).(map[*check.Package]map[*syntax.Ident]testDecl)
	if copies[p.Package] == nil {
		copies[p.Package] = duplicateTests(p)
	}
	for _, d := range p.File.Decls {
		var name *syntax.Ident
		switch d := d.(type) {
		case *syntax.TestDecl:
			name = d.Name
		case *syntax.LawDecl:
			name = d.Name
		}
		if kept, ok := copies[p.Package][name]; ok && name != nil {
			where := ""
			if kept.rel != p.Rel() {
				where = " in " + kept.rel
			}
			p.Report(name.Pos(), name.End(), name.Name+" does what "+kept.name.Name+where+" does")
		}
	}
}

// duplicateTests maps each test or law of p's package that copies another
// to the one kept.
func duplicateTests(p *Pass) map[*syntax.Ident]testDecl {
	var all []testDecl
	for _, u := range p.Program() {
		if u.Package != p.Package {
			continue
		}
		for _, f := range u.Files {
			if f.Syntax == nil {
				continue
			}
			for _, d := range f.Syntax.Decls {
				switch d := d.(type) {
				case *syntax.TestDecl:
					all = append(all, testDecl{f.Rel, d.Name, "test " + shapeOf(p.Info, syntax.Node(d.Params), syntax.Node(d.Body))})
				case *syntax.LawDecl:
					if d.Body != nil && d.Derived == nil && d.Pair == nil {
						all = append(all, testDecl{f.Rel, d.Name, "law " + shapeOf(p.Info, syntax.Node(d.Params), syntax.Node(d.Body))})
					}
				}
			}
		}
	}
	slices.SortStableFunc(all, func(a, b testDecl) int {
		return cmp.Or(cmp.Compare(a.rel, b.rel), cmp.Compare(a.name.Name, b.name.Name))
	})
	kept := map[string]testDecl{}
	out := map[*syntax.Ident]testDecl{}
	for _, t := range all {
		if k, ok := kept[t.shape]; ok {
			out[t.name] = k
			continue
		}
		kept[t.shape] = t
	}
	return out
}
