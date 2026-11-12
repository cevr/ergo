package lint

import "ergo.local/prototype/internal/sketch/syntax"

// bannedWords is the strict naming rule (effect-oxlint
// noShapeInSymbolNames): a declared name that holds a word the team bans
// as vague. Option words, default ["shape"]. It reads the names the
// checker resolved as declarations (Info.Defs) through words.go.
var bannedWords = &Rule{
	Name:    "banned-words",
	Code:    "L111",
	Doc:     "A declared name holds a word the configuration bans (words, default shape); name what the thing is.",
	Default: Warning,
	Check:   checkBannedWords,
	Options: options(func() wordOptions { return wordOptions{Words: []string{"shape"}} }, nil),
}

func checkBannedWords(p *Pass) {
	if p.Package.Std() || p.Package.Host() {
		return
	}
	words := p.Options.(wordOptions).Words
	syntax.Inspect(p.File, func(n syntax.Node) bool {
		id, ok := n.(*syntax.Ident)
		if !ok || p.Info.Defs[id] == nil {
			return true
		}
		if word, banned := bannedWord(id.Name, words); banned {
			p.Report(id.Pos(), id.End(), "the name "+id.Name+" holds the banned word "+word+"; name what the thing is")
		}
		return true
	})
}
