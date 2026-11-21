package lint

// narrationComment is the advisory rule for a comment that says what the
// code line under it does (lint-hygiene spec §4, the exact control that
// tied the Fable network): a // comment, alone on its line, with no
// comment line above it, directly above a line of code inside a function,
// test or law body, that shares two or more words with that line, and at
// least half of its own words. A word is a run of three or more letters,
// split at camelCase and lowercased; the code line's string literals and
// trailing comment are not its words.
//
// A doc comment is out of scope: it sits outside every body, so a
// member's doc comment (an operation's, a law's in an operation type),
// which repeats the member's name by convention, never fires (the 20
// false findings of the 2026-11-17 read). So is a comment of two or more
// lines, which explains more than one line restates.
var narrationComment = &Rule{
	Name:    "narration-comment",
	Code:    "L142",
	Doc:     "A comment that repeats the words of the code line under it tells a reader nothing the line does not; say why, or delete it.",
	Instead: "delete the comment, or replace it with why the line is there",
	Default: Hint,
	Check:   checkNarrationComment,
}

func checkNarrationComment(p *Pass) {
	src := p.File.Src
	bodies := bodySpans(p.File)
	for _, g := range p.File.Comments {
		for _, c := range g.List {
			at := int(c.Pos())
			if !lineComment(c) || !aloneOnLine(src, at) || !inBody(bodies, c.Pos()) {
				continue
			}
			start, end := lineAt(src, at)
			if start > 0 {
				prevStart, _ := lineAt(src, start-1)
				if kindOf(src, prevStart, start-1) == lineHoldsComment {
					continue
				}
			}
			if end >= len(src) {
				continue
			}
			nextStart, nextEnd := lineAt(src, end+1)
			if kindOf(src, nextStart, nextEnd) != lineCode {
				continue
			}
			words := textWords(nil, c.Text)
			shared := sharedWords(words, codeWords(src, nextStart, nextEnd))
			if shared >= 2 && 2*shared >= len(words) {
				p.Report(c.Pos(), c.End(), "this comment repeats the words of the line under it")
			}
		}
	}
}
