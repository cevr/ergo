package lint

import (
	"unicode"
	"unicode/utf8"

	"ergo.local/prototype/internal/sketch/syntax"
)

// The comment readers of the advisory rules (LH1): a comment's own line,
// its words and the words of the code line under it. They read bytes of
// the file's source at the positions the parser gave; no rule compares a
// spelling with a constant.

// lineAt is the bounds [start, end) of the source line that holds pos,
// without its newline.
func lineAt(src string, pos int) (int, int) {
	start := pos
	for start > 0 && src[start-1] != '\n' {
		start--
	}
	end := pos
	for end < len(src) && src[end] != '\n' {
		end++
	}
	return start, end
}

// lineComment reports a comment written with //.
func lineComment(c *syntax.Comment) bool {
	return len(c.Text) >= 2 && c.Text[0] == '/' && c.Text[1] == '/'
}

// aloneOnLine reports whether only spaces stand before pos on its line.
func aloneOnLine(src string, pos int) bool {
	start, _ := lineAt(src, pos)
	for i := start; i < pos; i++ {
		if src[i] != ' ' && src[i] != '\t' {
			return false
		}
	}
	return true
}

// lineKind is what a whole source line holds: nothing, a comment only, or
// code.
type lineKind int

const (
	lineBlank lineKind = iota
	lineHoldsComment
	lineCode
)

// kindOf is the kind of the line [start, end).
func kindOf(src string, start, end int) lineKind {
	for i := start; i < end; i++ {
		switch src[i] {
		case ' ', '\t', '\r':
			continue
		case '/':
			if i+1 < end && (src[i+1] == '/' || src[i+1] == '*') {
				return lineHoldsComment
			}
		}
		return lineCode
	}
	return lineBlank
}

// textWords is the words of s: runs of letters, split where a lower-case
// letter meets an upper-case one, lowercased, of three letters or more.
func textWords(dst []string, s string) []string {
	start := -1
	flush := func(end int) {
		if start >= 0 && end-start >= 3 {
			w := make([]rune, 0, end-start)
			for _, r := range s[start:end] {
				w = append(w, unicode.ToLower(r))
			}
			dst = append(dst, string(w))
		}
		start = -1
	}
	prevLower := false
	for i, r := range s {
		if !unicode.IsLetter(r) {
			flush(i)
			prevLower = false
			continue
		}
		if start >= 0 && unicode.IsUpper(r) && prevLower {
			flush(i)
		}
		if start < 0 {
			start = i
		}
		prevLower = unicode.IsLower(r)
	}
	flush(len(s))
	return dst
}

// codeWords is the words of the code on the line [start, end): string and
// rune literals and a trailing comment left out.
func codeWords(src string, start, end int) []string {
	var out []string
	from := start
	for i := start; i < end; {
		switch b := src[i]; {
		case b == '"' || b == '\'' || b == '`':
			out = textWords(out, src[from:i])
			i++
			for i < end && src[i] != b {
				if src[i] == '\\' && b != '`' {
					i++
				}
				i++
			}
			i++
			from = i
		case b == '/' && i+1 < end && src[i+1] == '/':
			return textWords(out, src[from:i])
		default:
			_, size := utf8.DecodeRuneInString(src[i:])
			i += size
		}
	}
	if from < end {
		out = textWords(out, src[from:end])
	}
	return out
}

// sharedWords is how many of the comment's words the code line holds.
func sharedWords(comment, code []string) int {
	n := 0
	for _, w := range comment {
		for _, c := range code {
			if w == c {
				n++
				break
			}
		}
	}
	return n
}

// docComments is the comment groups the file's declarations, fields,
// context entries and machine entries carry as their doc comments.
func docComments(f *syntax.File) map[*syntax.CommentGroup]bool {
	out := map[*syntax.CommentGroup]bool{}
	add := func(g *syntax.CommentGroup) {
		if g != nil {
			out[g] = true
		}
	}
	syntax.Inspect(f, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.ImportDecl:
			add(n.Doc)
		case *syntax.VarDecl:
			add(n.Doc)
		case *syntax.TypeDecl:
			add(n.Doc)
		case *syntax.FuncDecl:
			add(n.Doc)
		case *syntax.ContextDecl:
			add(n.Doc)
		case *syntax.ContextEntry:
			add(n.Doc)
		case *syntax.Field:
			add(n.Doc)
		case *syntax.MachineEntry:
			add(n.Doc)
		case *syntax.TestDecl:
			add(n.Doc)
		case *syntax.LawDecl:
			add(n.Doc)
		}
		return true
	})
	return out
}

// bodySpans is the byte spans of the bodies of the file's functions,
// function literals, tests and laws: the code a body comment sits in. A
// member's doc comment (an operation's, a law's in an operation type) is
// outside every body.
func bodySpans(f *syntax.File) [][2]syntax.Pos {
	var out [][2]syntax.Pos
	add := func(b *syntax.BlockStmt) {
		if b != nil {
			out = append(out, [2]syntax.Pos{b.Pos(), b.End()})
		}
	}
	syntax.Inspect(f, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.FuncDecl:
			add(n.Body)
		case *syntax.FuncLit:
			add(n.Body)
		case *syntax.TestDecl:
			add(n.Body)
		case *syntax.LawDecl:
			add(n.Body)
		}
		return true
	})
	return out
}

// inBody reports whether pos is inside one of the spans.
func inBody(spans [][2]syntax.Pos, pos syntax.Pos) bool {
	for _, s := range spans {
		if s[0] < pos && pos < s[1] {
			return true
		}
	}
	return false
}

// commentBody is the group's text without its comment marks: each //
// comment's slashes and one space after them, each /* */ comment's
// delimiters, one line per comment.
func commentBody(g *syntax.CommentGroup) string {
	out := ""
	for i, c := range g.List {
		if i > 0 {
			out += "\n"
		}
		text := c.Text[2:]
		if !lineComment(c) {
			text = text[:len(text)-2]
		}
		if len(text) > 0 && text[0] == ' ' {
			text = text[1:]
		}
		out += text
	}
	return out
}

// parsesAsCode reports whether text parses as the statements of a body,
// not all of them a lone name or literal, or as one or more declarations.
func parsesAsCode(text string) bool {
	if f, err := syntax.Parse("func commented() {\n" + text + "\n}\n"); err == nil && len(f.Decls) == 1 {
		if fn, ok := f.Decls[0].(*syntax.FuncDecl); ok && fn.Body != nil {
			for _, s := range fn.Body.List {
				if !loneOperand(s) {
					return true
				}
			}
		}
	}
	f, err := syntax.Parse(text + "\n")
	return err == nil && len(f.Decls) > 0
}

// loneOperand reports a statement that is only a name, a literal or a
// selector of names: prose of one word parses as one.
func loneOperand(s syntax.Stmt) bool {
	e, ok := s.(*syntax.ExprStmt)
	if !ok {
		return false
	}
	for {
		switch x := e.X.(type) {
		case *syntax.Ident, *syntax.BasicLit:
			return true
		case *syntax.SelectorExpr:
			e = &syntax.ExprStmt{X: x.X}
		default:
			return false
		}
	}
}
