package lint

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// consoleOutsideEntry is the layout rule for stray output (catalog CR30,
// debug logging): an operation of the prelude's Console outside the entry
// file (cmd/NAME/main.ergo) and outside tests and laws. Every function
// writes its rows (rows.md, Capability rows 6), so a `needs Console`
// deep in a package spreads to every caller up to the entry; output
// chosen at the entry keeps the package's functions returning values the
// caller can test and print.
var consoleOutsideEntry = &Rule{
	Name:    "console-outside-entry",
	Code:    "L134",
	Doc:     "A Console write outside cmd/NAME/main.ergo prints from inside a package: a debug line, or output its callers cannot test or redirect.",
	Instead: "return the text to the caller and print it in cmd/NAME/main.ergo",
	Default: Warning,
	Check:   checkConsoleOutsideEntry,
}

func checkConsoleOutsideEntry(p *Pass) {
	if entryFile(p.Rel()) || testFile(p.File) {
		return
	}
	syntax.Inspect(p.File, func(n syntax.Node) bool {
		switch x := n.(type) {
		case *syntax.TestDecl, *syntax.LawDecl:
			return false
		case *syntax.CallExpr:
			if t := check.ProviderOpType(p.Info, x); t != nil && check.IsConsole(t) {
				p.Report(x.Pos(), x.End(), "this Console write is outside the entry file")
			}
		}
		return true
	})
}
