package lint

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// unusedDeclaration is the dead-code rule of the strict preset (catalog
// CR25): an unexported top-level function, type or let that no other
// declaration of its package uses. The checker reports an unused local
// or import (S110); a package-level declaration only its own body names
// is dead the same way. A use from a test or a law counts. The entry
// function, a method that provides an operation of a type's implements
// clause (a call of the operation reaches it), bindings of Go (extern) and the
// declarations the parser derives are out of scope.
var unusedDeclaration = &Rule{
	Name:    "unused-declaration",
	Code:    "L114",
	Doc:     "An unexported top-level function, type or let that nothing else in its package uses is dead code; delete it.",
	Instead: "delete it",
	Default: Warning,
	Check:   checkUnusedDeclaration,
}

func checkUnusedDeclaration(p *Pass) {
	used := p.Memo(func() any { return map[*check.Package]map[check.Object]bool{} }).(map[*check.Package]map[check.Object]bool)
	if used[p.Package] == nil {
		used[p.Package] = usedElsewhere(p)
	}
	entry := p.Package.Entry()
	report := func(id *syntax.Ident) {
		o := p.Info.Defs[id]
		if o == nil || exportedName(id) || used[p.Package][o] {
			return
		}
		if f, ok := o.(*check.Func); ok && (f == entry || f.ProvidesOperation()) {
			return
		}
		p.Report(id.Pos(), id.End(), id.Name+" is declared and never used in its package")
	}
	for _, d := range p.File.Decls {
		switch d := d.(type) {
		case *syntax.FuncDecl:
			if d.Recv == nil && d.Derived == nil && d.Extern == nil && d.Body != nil {
				report(d.Name)
			}
		case *syntax.TypeDecl:
			if d.Extern == nil && d.Origin == nil {
				report(d.Name)
			}
		case *syntax.VarDecl:
			if d.Extern == nil {
				for _, s := range d.Specs {
					for _, id := range s.Names {
						report(id)
					}
				}
			}
		}
	}
}

// usedElsewhere is the objects of p's package that a top-level
// declaration other than their own names.
func usedElsewhere(p *Pass) map[check.Object]bool {
	used := map[check.Object]bool{}
	own := map[check.Object]bool{}
	var seen []check.Object
	for _, u := range p.Program() {
		if u.Package != p.Package {
			continue
		}
		for _, f := range u.Files {
			if f.Syntax == nil {
				continue
			}
			fi := p.run.shared.fileIndexOf(f.Syntax)
			for _, dr := range fi.decls {
				clear(own)
				seen = seen[:0]
				for _, x := range fi.idents[dr.identStart:dr.identEnd] {
					if def := p.Info.Defs[x]; def != nil {
						own[def] = true
					}
					if o := p.Info.Uses[x]; o != nil {
						seen = append(seen, o)
					}
				}
				for _, x := range fi.calls[dr.callStart:dr.callEnd] {
					// a dot call, p.f(…), reaches f through the
					// call, not through a use of its name
					if c := p.Info.Calls[x]; c != nil && c.Func != nil {
						seen = append(seen, c.Func)
					}
				}
				for _, o := range seen {
					if o != nil && !own[o] {
						used[o] = true
					}
				}
			}
		}
	}
	return used
}
