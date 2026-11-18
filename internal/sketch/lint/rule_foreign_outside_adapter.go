package lint

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// foreignOutsideAdapter is the layout rule for vendor code behind named
// adapters (catalog EF18 and EF32; AGENTS.md: "the domain package
// declares the contracts it needs (operation types with laws);
// implementations live in packages named after their dependency"): an
// `import go` of a package outside the reviewed pure list (math,
// unicode and the like reach no vendor and no host) in a package that
// declares an operation type. That package
// is a domain package, and the Go it imports is an implementation that
// belongs in an adapter package that provides the operation type.
var foreignOutsideAdapter = &Rule{
	Name:    "foreign-outside-adapter",
	Code:    "L132",
	Doc:     "An import go in a package that declares an operation type puts a vendor's code in the domain; keep it in an adapter package that implements the contract.",
	Instead: "move the code that uses the Go package into an adapter package named after it, which implements the operation type, and bind it at the entry",
	Default: Warning,
	Check:   checkForeignOutsideAdapter,
}

func checkForeignOutsideAdapter(p *Pass) {
	if !declaresOperationType(p) {
		return
	}
	for _, im := range p.Package.ImportsOf(p.File) {
		if !check.IsGoImport(im.Pkg) || check.PureGoPackage(im.Pkg.Path) {
			continue
		}
		p.Report(im.Spec.Pos(), im.Spec.End(), "this package declares an operation type, a contract, and imports Go code that an adapter should hold")
	}
}

// declaresOperationType reports a package with an operation type among
// its declarations.
func declaresOperationType(p *Pass) bool {
	for _, u := range p.Program() {
		if u.Package != p.Package {
			continue
		}
		for _, f := range u.Files {
			if f.Syntax == nil {
				continue
			}
			for _, d := range f.Syntax.Decls {
				td, ok := d.(*syntax.TypeDecl)
				if !ok {
					continue
				}
				if tn, ok := p.Info.Defs[td.Name].(*check.TypeName); ok && check.OperationType(tn) {
					return true
				}
			}
		}
	}
	return false
}
