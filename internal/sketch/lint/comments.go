package lint

import (
	"strings"
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
			seg := s[start:end]
			lowerASCII := true
			ascii := true
			for j := 0; j < len(seg); j++ {
				b := seg[j]
				if b >= utf8.RuneSelf {
					ascii = false
					lowerASCII = false
					break
				}
				if b >= 'A' && b <= 'Z' {
					lowerASCII = false
				}
			}
			if lowerASCII {
				dst = append(dst, seg)
			} else if ascii {
				var buf [64]byte
				b := buf[:0]
				for j := 0; j < len(seg); j++ {
					c := seg[j]
					if c >= 'A' && c <= 'Z' {
						c += 'a' - 'A'
					}
					b = append(b, c)
				}
				dst = append(dst, string(b))
			} else {
				w := make([]rune, 0, end-start)
				for _, r := range seg {
					w = append(w, unicode.ToLower(r))
				}
				dst = append(dst, string(w))
			}
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
	walkNode(f, func(n syntax.Node) bool {
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
	walkNode(f, func(n syntax.Node) bool {
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
	return mergeBodySpans(out)
}

// inBody reports whether pos is inside one of the sorted merged spans.
func inBody(spans [][2]syntax.Pos, pos syntax.Pos) bool {
	lo, hi := 0, len(spans)
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		if spans[mid][0] < pos {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo > 0 && pos < spans[lo-1][1]
}

// commentBody is the group's text without its comment marks: each //
// comment's slashes and one space after them, each /* */ comment's
// delimiters, one line per comment.
func commentBody(g *syntax.CommentGroup) string {
	var b strings.Builder
	for i, c := range g.List {
		if i > 0 {
			b.WriteByte('\n')
		}
		text := c.Text[2:]
		if !lineComment(c) {
			text = text[:len(text)-2]
		}
		if len(text) > 0 && text[0] == ' ' {
			text = text[1:]
		}
		b.WriteString(text)
	}
	return b.String()
}

// parsesAsCode reports whether text parses as the statements of a body,
// not all of them a lone name or literal, or as one or more declarations.
func parsesAsCode(text string) bool {
	if looksLikeProse(text) {
		return false
	}
	if f, err := syntax.Parse("func commented() {\n" + text + "\n}\n"); err == nil && len(f.Decls) == 1 {
		if fn, ok := f.Decls[0].(*syntax.FuncDecl); ok && fn.Body != nil {
			for _, s := range fn.Body.List {
				if !loneOperand(s) {
					return true
				}
			}
		}
	}
	if !startsWithDeclLead(text) {
		return false
	}
	f, err := syntax.Parse(text + "\n")
	return err == nil && len(f.Decls) > 0
}

// looksLikeProse fast-rejects comments that cannot parse as valid Ergo
// statements or declarations: empty text, or a first line that opens with
// two space-separated identifiers where neither can start/continue an Ergo
// construct (e.g. English sentences).
func looksLikeProse(text string) bool {
	i := 0
	for i < len(text) && (text[i] == ' ' || text[i] == '\t' || text[i] == '\n' || text[i] == '\r') {
		i++
	}
	if i >= len(text) {
		return true
	}
	c := text[i]
	if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_') {
		return false
	}
	w1Start := i
	i++
	for i < len(text) && ((text[i] >= 'a' && text[i] <= 'z') || (text[i] >= 'A' && text[i] <= 'Z') || (text[i] >= '0' && text[i] <= '9') || text[i] == '_') {
		i++
	}
	w1 := text[w1Start:i]
	spStart := i
	for i < len(text) && (text[i] == ' ' || text[i] == '\t') {
		i++
	}
	if i == spStart || i >= len(text) {
		return false
	}
	c = text[i]
	if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_') {
		return false
	}
	w2Start := i
	i++
	for i < len(text) && ((text[i] >= 'a' && text[i] <= 'z') || (text[i] >= 'A' && text[i] <= 'Z') || (text[i] >= '0' && text[i] <= '9') || text[i] == '_') {
		i++
	}
	w2 := text[w2Start:i]
	if syntax.IsKeyword(w1) || syntax.IsKeyword(w2) {
		return false
	}
	switch w1 {
	case "always", "sometimes", "die", "skip", "entry", "pure", "never", "reaches", "path", "when", "own", "replace", "defects", "initial", "step", "after", "work", "exit":
		return false
	}
	switch w2 {
	case "implements", "where", "derive", "when", "reaches", "defects":
		return false
	}
	return true
}

// startsWithDeclLead reports whether the first non-whitespace token of text
// can start a top-level Ergo declaration or comment.
func startsWithDeclLead(text string) bool {
	i := 0
	for i < len(text) && (text[i] == ' ' || text[i] == '\t' || text[i] == '\n' || text[i] == '\r') {
		i++
	}
	if i >= len(text) {
		return false
	}
	if text[i] == '/' && i+1 < len(text) && (text[i+1] == '/' || text[i+1] == '*') {
		return true
	}
	end := i
	for end < len(text) && (text[end] >= 'a' && text[end] <= 'z') {
		end++
	}
	if end == i {
		return false
	}
	if end < len(text) && (text[end] >= 'A' && text[end] <= 'Z' || text[end] >= '0' && text[end] <= '9' || text[end] == '_') {
		return false
	}
	switch text[i:end] {
	case "import", "let", "type", "func", "context", "test", "law", "extern", "entry":
		return true
	}
	return false
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
