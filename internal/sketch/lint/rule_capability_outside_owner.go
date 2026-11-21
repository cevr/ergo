package lint

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// capabilityOutsideOwner is the layout rule for transport kept in its
// owner (catalog EF31, a braid: transport inside a feature; AD5, clients
// never read an aggregate): an operation of a capability that option
// owners lists, run outside the folders it names. The option maps a
// capability, as the type prints (`store.Store`, `Console`), to the
// package folders under the module root that may run its operations; a
// capability it does not list is out of scope, so with no option the
// rule finds nothing. Tests and laws are out of scope: they exercise the
// capability.
var capabilityOutsideOwner = &Rule{
	Name:    "capability-outside-owner",
	Code:    "L133",
	Doc:     "An operation of a capability that the owners option assigns to other packages runs it outside its owner.",
	Instead: "call a function of the owner package that runs the operation, and keep the capability's operations in the packages the owners option names",
	Default: Warning,
	Check:   checkCapabilityOutsideOwner,
	Options: options(func() ownerOptions { return ownerOptions{} }, nil),
}

func checkCapabilityOutsideOwner(p *Pass) {
	owners := p.Options.(ownerOptions).Owners
	if len(owners) == 0 || testFile(p.File) {
		return
	}
	syntax.Inspect(p.File, func(n syntax.Node) bool {
		switch x := n.(type) {
		case *syntax.TestDecl, *syntax.LawDecl:
			return false
		case *syntax.CallExpr:
			t := check.ProviderOpType(p.Info, x)
			if t == nil {
				return true
			}
			if dirs, listed := ownersOf(owners, t); listed && !ownedHere(p.Rel(), dirs) {
				p.Report(x.Pos(), x.End(), "this runs an operation of "+t.String()+" outside the packages that own it")
			}
		}
		return true
	})
}
