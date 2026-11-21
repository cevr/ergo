package lint

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// consoleOutsideEntry is the layout rule for stray output (catalog CR30,
// debug logging; EF31, transport inside a feature): an operation of the
// prelude's Console in a domain package, one that declares an operation
// type and has no main, outside its tests and laws. A host package that
// plays a script and prints (examples' script, screen and console
// packages) declares no contract and is out of scope, as is the entry.
// Every function writes its rows (rows.md, Capability rows 6), so a
// `needs Console` in the domain spreads to every caller up to the entry.
var consoleOutsideEntry = &Rule{
	Name:    "console-outside-entry",
	Code:    "L134",
	Doc:     "A Console write in a package that declares an operation type prints from the domain: a debug line, or output its callers cannot test or redirect.",
	Instead: "return the text to the caller and print it in the entry or a host package",
	Default: Warning,
	Check:   checkConsoleOutsideEntry,
}

func checkConsoleOutsideEntry(p *Pass) {
	if testFile(p.File) || p.Package.Entry() != nil || !declaresOperationType(p) {
		return
	}
	syntax.Inspect(p.File, func(n syntax.Node) bool {
		switch x := n.(type) {
		case *syntax.TestDecl, *syntax.LawDecl:
			return false
		case *syntax.CallExpr:
			if t := check.ProviderOpType(p.Info, x); t != nil && check.IsConsole(t) {
				p.Report(x.Pos(), x.End(), "this Console write is in a package that declares an operation type")
			}
		}
		return true
	})
}
