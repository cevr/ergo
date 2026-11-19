package lint

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// loneProvider is the indirection rule of the strict preset (catalog
// CR17, an abstraction only for future reuse; anti-slop
// single-use-abstraction, shallow-service): an operation type of the
// program that exactly one type implements, with no law, its own or a
// top-level law over it. A second provider (a test double counts) or a
// law makes it a contract; with neither it is a second name for its one
// provider. An exported operation type is reported only when the run's
// root has main, so every provider is in the run (as unbuilt-variant).
var loneProvider = &Rule{
	Name:    "lone-provider",
	Code:    "L122",
	Doc:     "An operation type with one provider and no law is an indirection with nothing behind it; use the provider, or state the laws.",
	Instead: "use the provider directly, or state the operation type's laws so a second provider is held to them",
	Default: Warning,
	Check:   checkLoneProvider,
}

// lone is an operation type's one provider.
type lone struct{ provider *check.TypeName }

func checkLoneProvider(p *Pass) {
	lones := p.Memo(func() any { return loneProviders(p) }).(map[*check.TypeName]lone)
	for _, d := range p.File.Decls {
		t, ok := d.(*syntax.TypeDecl)
		if !ok {
			continue
		}
		tn, ok := p.Info.Defs[t.Name].(*check.TypeName)
		if !ok {
			continue
		}
		l, found := lones[tn]
		if !found || exportedName(t.Name) && !p.Closed() {
			continue
		}
		p.ReportInstead(t.Name.Pos(), t.Name.End(), t.Name.Name+" has one provider, "+l.provider.Name+", and no law",
			"use "+l.provider.Name+" where "+t.Name.Name+" is named, or state "+t.Name.Name+"'s laws so a second provider is held to them", nil)
	}
}

// loneProviders maps each operation type of the program with exactly one
// provider and no law to that provider.
func loneProviders(p *Pass) map[*check.TypeName]lone {
	providers := map[*check.TypeName][]*check.TypeName{}
	// a law's slots name its owner first, so a member law counts too
	lawful := map[*check.TypeName]bool{}
	var ops []*check.TypeName
	for _, u := range p.Program() {
		info := u.Package.Info
		for _, law := range u.Package.Laws {
			for _, s := range law.Slots {
				if s.Type != nil {
					lawful[origin(s.Type).Obj] = true
				}
			}
		}
		for _, f := range u.Files {
			if f.Syntax == nil {
				continue
			}
			for _, d := range f.Syntax.Decls {
				t, ok := d.(*syntax.TypeDecl)
				if !ok {
					continue
				}
				tn, ok := info.Defs[t.Name].(*check.TypeName)
				if !ok {
					continue
				}
				if check.OperationType(tn) {
					ops = append(ops, tn)
				}
				n, ok := tn.Type.(*check.Named)
				if !ok {
					continue
				}
				for _, impl := range n.Implements() {
					op := origin(impl).Obj
					providers[op] = append(providers[op], tn)
				}
			}
		}
	}
	out := map[*check.TypeName]lone{}
	for _, op := range ops {
		if len(providers[op]) == 1 && !lawful[op] {
			out[op] = lone{providers[op][0]}
		}
	}
	return out
}
