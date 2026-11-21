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
// unexported variant's builders are certain to be in it. A property's or
// a law's generator builds every type its parameters reach, and a
// generic call (a decoder) every type its type arguments reach.
var unbuiltVariant = &Rule{
	Name:    "unbuilt-variant",
	Code:    "L120",
	Doc:     "A record variant of a union that no code builds is a state the domain does not have; delete it or build it.",
	Instead: "delete it, or build it where the domain reaches that state",
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
		p.Report(id.Pos(), id.End(), "no code builds "+id.Name+", a variant of a union")
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

// builtNames marks the types that n names as values: a variant with no
// fields is built by its bare name (`to(Ground, …)`). A name in a type
// position (a field, parameter or let's type, a case or catch pattern, a
// literal's type) builds nothing, so those subtrees are skipped.
func builtNames(info *check.Info, n syntax.Node, built map[*check.TypeName]bool) {
	syntax.Inspect(n, func(n syntax.Node) bool {
		switch x := n.(type) {
		case *syntax.Field, *syntax.FuncType, *syntax.TypeDecl:
			return false
		case *syntax.VarSpec:
			for _, v := range x.Values {
				builtNames(info, v, built)
			}
			return false
		case *syntax.CaseClause:
			for _, s := range x.Body {
				builtNames(info, s, built)
			}
			return false
		case *syntax.CatchArm:
			for _, s := range x.Body {
				builtNames(info, s, built)
			}
			return false
		case *syntax.CompositeLit:
			for _, e := range x.Elts {
				builtNames(info, e, built)
			}
			return false
		case *syntax.Ident:
			if tn, ok := info.Uses[x].(*check.TypeName); ok {
				built[tn] = true
			}
		case *syntax.SelectorExpr:
			// pkg.Variant: a value of another package's variant
			if id, ok := x.X.(*syntax.Ident); ok {
				if _, pkg := info.Uses[id].(*check.PkgName); pkg {
					if n, ok := info.Types[x].(*check.Named); ok {
						built[origin(n).Obj] = true
					}
					return false
				}
			}
		}
		return true
	})
}

// generated marks every type reachable from a property's or a law's
// parameters: the generator builds values of each (tests.go rule 3).
func generated(info *check.Info, params *syntax.FieldList, built map[*check.TypeName]bool) {
	if params == nil {
		return
	}
	for _, f := range params.List {
		for _, id := range f.Names {
			if v, ok := info.Defs[id].(*check.Var); ok && v.Type != nil {
				for _, tn := range check.Mentions(v.Type, nil) {
					built[tn] = true
				}
			}
		}
	}
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
				case *syntax.CallExpr:
					// a generic call given a type, such as a decoder, can
					// build every type that type reaches
					if c := info.Calls[x]; c != nil {
						for _, a := range c.TypeArgs {
							for _, tn := range check.Mentions(a, nil) {
								facts.built[tn] = true
							}
						}
					}
				case *syntax.FuncDecl:
					if x.Body != nil {
						builtNames(info, x.Body, facts.built)
					}
				case *syntax.TestDecl:
					builtNames(info, x.Body, facts.built)
					generated(info, x.Params, facts.built)
				case *syntax.LawDecl:
					if x.Body != nil {
						builtNames(info, x.Body, facts.built)
					}
					generated(info, x.Params, facts.built)
				case *syntax.VarDecl:
					for _, s := range x.Specs {
						for _, v := range s.Values {
							builtNames(info, v, facts.built)
						}
					}
				}
				return true
			})
		}
	}
	return facts
}
