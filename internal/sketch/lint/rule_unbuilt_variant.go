package lint

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// unbuiltVariant is the rule of the strict preset for a state the domain
// does not have (catalog CR4): a record variant of a union, declared in
// the program, that no composite literal of the program builds. Every
// match over the union still handles it, so the dead case costs every
// reader. The run must see every builder: when the run's root has an
// entry (Pass.Closed), it sees the whole program; otherwise only an
// unexported variant's builders are certain to be in it.
var unbuiltVariant = &Rule{
	Name:    "unbuilt-variant",
	Code:    "L120",
	Doc:     "A record variant of a union that no code builds is a state the domain does not have; delete it or build it.",
	Default: Warning,
	Check:   checkUnbuiltVariant,
}

// variantFacts are the program's union variants and the types its
// composite literals build.
type variantFacts struct {
	variants map[*check.TypeName]bool
	built    map[*check.TypeName]bool
}

func checkUnbuiltVariant(p *Pass) {
	if p.Package.Std() || p.Package.Host() {
		return
	}
	facts := p.Memo(func() any { return variantsOf(p) }).(variantFacts)
	closed := p.Closed()
	report := func(id *syntax.Ident) {
		tn, ok := p.Info.Defs[id].(*check.TypeName)
		if !ok || !facts.variants[tn] || facts.built[tn] || !closed && exportedName(id) {
			return
		}
		if _, record := underlying(tn).(*check.Struct); !record {
			return
		}
		p.Report(id.Pos(), id.End(), "no code builds "+id.Name+", a variant of a union; delete it, or build it where the domain reaches that state")
	}
	syntax.Inspect(p.File, func(n syntax.Node) bool {
		switch x := n.(type) {
		case *syntax.TypeDecl:
			if x.TypeParams == nil && x.Extern == nil && x.Origin == nil {
				report(x.Name)
			}
		case *syntax.VariantSpec:
			if x.TypeArgs == nil {
				report(x.Name)
			}
		case *syntax.FuncDecl, *syntax.TestDecl, *syntax.LawDecl:
			return false
		}
		return true
	})
}

// variantsOf reads the program's union variants and built types.
func variantsOf(p *Pass) variantFacts {
	facts := variantFacts{variants: map[*check.TypeName]bool{}, built: map[*check.TypeName]bool{}}
	for _, u := range p.Program() {
		info := u.Package.Info
		for _, f := range u.Files {
			if f.Syntax == nil {
				continue
			}
			syntax.Inspect(f.Syntax, func(n syntax.Node) bool {
				switch x := n.(type) {
				case *syntax.TypeDecl:
					if tn, ok := info.Defs[x.Name].(*check.TypeName); ok {
						if union, ok := underlying(tn).(*check.Union); ok && union.Row == nil {
							for _, v := range union.Variants {
								if n, ok := v.(*check.Named); ok {
									facts.variants[origin(n).Obj] = true
								}
							}
						}
					}
				case *syntax.CompositeLit:
					if n, ok := info.Types[x].(*check.Named); ok {
						facts.built[origin(n).Obj] = true
					}
				}
				return true
			})
		}
	}
	return facts
}
