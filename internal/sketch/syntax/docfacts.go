package syntax

import (
	"regexp"
	"strings"
	"sync"
)

// Doc-comment facts. A declaration's doc comment can state a fact about it
// in a paragraph that starts with a fixed word and a colon, as Go's
// "Deprecated:" convention does (go.dev/wiki/Deprecated; staticcheck
// SA1019). The parser keeps the comment; these methods read the fact, so a
// reader (lint) matches the declaration a use resolves to and never the
// comment's text.

// Generated reports a file that a tool wrote: a // comment that is the
// whole line `// Code generated … DO NOT EDIT.` (Go's convention, go help
// generate; Go's ast.IsGenerated) among the comments before the first
// declaration, which `ergo bind go` and `ergo gen` put first. Such a
// comment after code, or one that only holds the words, is not the mark.
func (f *File) Generated() bool {
	for _, g := range f.Comments {
		for _, c := range g.List {
			if len(f.Decls) > 0 && c.End() > f.Decls[0].Pos() {
				return false
			}
			if generatedLine().MatchString(c.Text) {
				return true
			}
		}
	}
	return false
}

// generatedLine is Go's generated-file mark, matched against a whole
// comment.
var generatedLine = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^// Code generated .* DO NOT EDIT\.$`) })

// Deprecated is the paragraph of the doc comment that starts "Deprecated:",
// and whether there is one: the declaration is kept for old callers and
// should not be used.
func (g *CommentGroup) Deprecated() (string, bool) { return g.paragraph("Deprecated:") }

// Unbounded is the paragraph of the doc comment that starts "Unbounded:",
// and whether there is one: the Seq the declaration gives does not end by
// itself (it ends only when another task closes its source), so reading it
// to its end, as slices.Collect does, can wait for ever.
func (g *CommentGroup) Unbounded() (string, bool) { return g.paragraph("Unbounded:") }

// Scans is the paragraph of the doc comment that starts "Scans:", and
// whether there is one: the function walks the parameter the paragraph
// names first, element by element, so a call of it on every turn of a
// loop over the same collection does n² work. Any package can write it,
// as for a Go binding whose body Ergo cannot read (std slices.Contains).
func (g *CommentGroup) Scans() (string, bool) { return g.paragraph("Scans:") }

// paragraph returns the first paragraph whose first line starts with word,
// its lines joined by spaces.
func (g *CommentGroup) paragraph(word string) (string, bool) {
	if g == nil {
		return "", false
	}
	var lines []string
	for _, c := range g.List {
		text := c.Text
		switch {
		case strings.HasPrefix(text, "//"):
			lines = append(lines, text[2:])
		case strings.HasPrefix(text, "/*"):
			lines = append(lines, strings.Split(strings.TrimSuffix(text[2:], "*/"), "\n")...)
		}
	}
	var found []string
	start := true
	for _, l := range lines {
		l = strings.TrimSpace(l)
		switch {
		case l == "":
			if len(found) > 0 {
				return strings.Join(found, " "), true
			}
			start = true
		case len(found) > 0:
			found = append(found, l)
		case start && strings.HasPrefix(l, word):
			found = append(found, l)
		default:
			start = false
		}
	}
	return strings.Join(found, " "), len(found) > 0
}
