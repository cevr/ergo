package lint

import (
	"unicode"
	"unicode/utf8"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// withAtEntry is the layout rule for choosing implementations in one place
// (AGENTS.md: "each binary's cmd/<name>/main.ergo binds them in a context
// and is the only place that chooses them"; tsgo strictEffectProvide,
// effect-oxlint noInlineProvide). A with that converts a value of a
// program package's type into a capability picks the implementation; one
// that binds a capability value passed down picks nothing, and one of std
// providers (a virtual clock, a script) picks no program implementation.
// A package-level named context picks the same way, by its own entries and
// the contexts it embeds, wherever a with of it stands: its declaration is
// where the choice is written.
//
// The pick stands only in an entry file (cmd/NAME/main.ergo, or a program
// of one source text), in a test file, or in a function option functions
// lists as DIR.Name (DIR the package's folder under the module root:
// run.Listed). A test file is one that declares a test or a law, as Go's
// _test.go files hold tests and their helpers, or one that declares only
// unexported named contexts: the bindings of the test build's law runs
// (types.md, Laws 10, Under a context). No other package can name such a
// context, and a with of it in a function is reported at that with, so
// the run build never builds it unreported.
var withAtEntry = &Rule{
	Name:    "with-at-entry",
	Code:    "L112",
	Doc:     "A with or a named context that picks a program's implementation outside cmd/NAME/main.ergo or a file of tests and laws chooses it away from the entry; move the choice to the entry.",
	Default: Warning,
	Check:   checkWithAtEntry,
	Options: options(func() functionOptions { return functionOptions{} }, nil),
}

func checkWithAtEntry(p *Pass) {
	if p.Package.Std() || p.Package.Host() || entryFile(p.Rel()) || testFile(p.File) {
		return
	}
	listed := p.Options.(functionOptions).Functions
	for _, d := range p.File.Decls {
		if cd, ok := d.(*syntax.ContextDecl); ok {
			w := p.Package.Contexts[cd.Name.Name]
			if w == nil || w.Decl != cd {
				continue
			}
			if t := contextPick(w, map[*check.Context]bool{}); t != nil {
				p.Report(cd.Name.Pos(), cd.Name.End(), "this context picks a program implementation ("+t.String()+") away from the entry; bind it in cmd/NAME/main.ergo, or in a test file for the tests")
			}
			continue
		}
		f, ok := d.(*syntax.FuncDecl)
		if !ok || f.Body == nil {
			continue
		}
		if fn, _ := p.Info.Defs[f.Name].(*check.Func); listedFunction(p.Rel(), fn, listed) {
			continue
		}
		syntax.Inspect(f.Body, func(n syntax.Node) bool {
			x, ok := n.(*syntax.CallExpr)
			if !ok {
				return true
			}
			w := p.Info.Withs[x]
			if w == nil {
				return true
			}
			for _, t := range w.ImplementationTypes() {
				if check.ProgramType(t) {
					p.Report(x.Pos(), x.Fun.End(), "this with picks a program implementation ("+t.String()+") away from the entry; bind it in cmd/NAME/main.ergo and pass the capability down")
					break
				}
			}
			return true
		})
	}
}

// testFile reports a file of tests (withAtEntry): one that declares a
// test or a law, or only unexported named contexts.
func testFile(f *syntax.File) bool {
	for _, d := range f.Decls {
		switch d.(type) {
		case *syntax.TestDecl, *syntax.LawDecl:
			return true
		}
	}
	contexts := 0
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *syntax.ImportDecl:
		case *syntax.ContextDecl:
			if r, _ := utf8.DecodeRuneInString(d.Name.Name); unicode.IsUpper(r) {
				return false
			}
			contexts++
		default:
			return false
		}
	}
	return contexts > 0
}

// contextPick is the first program type that w converts into a capability
// by its own entries or by a context it embeds, at any depth; nil when it
// picks none.
func contextPick(w *check.Context, seen map[*check.Context]bool) check.Type {
	if w == nil || seen[w] {
		return nil
	}
	seen[w] = true
	for _, e := range w.Entries {
		if t := contextPick(e.Embedded, seen); t != nil {
			return t
		}
		for _, conv := range e.Conversions {
			if conv.Kind != 0 && conv.From != nil && check.ProgramType(conv.From) {
				return conv.From
			}
		}
	}
	return nil
}
