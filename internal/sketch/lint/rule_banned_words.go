package lint

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// bannedWords is the strict naming rule (effect-oxlint
// noShapeInSymbolNames): a declared name that holds a word the team bans
// as vague. Option words, default ["shape"]. Option verbs (AS4, Fowler
// mysterious-name): an unexported top-level function named by one
// generic verb alone, default genericVerbs, outside the entry package.
// An exported one is read with its package's name (keys.Run), which says
// what; the entry's run is the body of main by convention (the
// 2026-11-18 read sample: 108 of 123 findings were Run or run of these
// two kinds). Option glossary (SR-ERGO-3): a GLOSSARY.md whose _Avoid_
// terms no declared name may hold; the finding names the term to use.
// It reads the names the checker resolved
// as declarations (Info.Defs) through words.go.
var bannedWords = &Rule{
	Name:    "banned-words",
	Code:    "L111",
	Doc:     "A declared name holds a word the configuration bans (words, default shape), or a top-level function is named by a generic verb alone (verbs, default handle, process, run and the like); name what the thing is or does.",
	Instead: "name what the thing is, or what the function does to what",
	Default: Warning,
	Check:   checkBannedWords,
	Options: options(func() wordOptions { return wordOptions{Words: []string{"shape"}, Verbs: genericVerbs} }, nil),
}

func checkBannedWords(p *Pass) {
	words, avoid := p.Options.(wordOptions).Words, p.Options.(wordOptions).avoid
	for _, d := range p.File.Decls {
		f, ok := d.(*syntax.FuncDecl)
		if !ok || p.Package.Entry() != nil {
			continue
		}
		if fn, ok := p.Info.Defs[f.Name].(*check.Func); ok && !fn.Exported() && bareVerb(f.Name.Name, p.Options.(wordOptions).Verbs) {
			p.Report(f.Name.Pos(), f.Name.End(), "the function "+f.Name.Name+" is named by a generic verb alone")
		}
	}
	syntax.Inspect(p.File, func(n syntax.Node) bool {
		id, ok := n.(*syntax.Ident)
		if !ok || p.Info.Defs[id] == nil {
			return true
		}
		if word, banned := bannedWord(id.Name, words); banned {
			p.Report(id.Pos(), id.End(), "the name "+id.Name+" holds the banned word "+word)
		} else if t, avoided := avoidedTerm(id.Name, avoid); avoided {
			p.ReportInstead(id.Pos(), id.End(), "the name "+id.Name+" holds "+t.Avoid+", which the glossary lists under Avoid", "name it with the glossary's term "+t.Use, nil)
		}
		return true
	})
}
