package bindc

import "strings"

type tokKind int

const (
	tEOF tokKind = iota
	tIdent
	tNumber
	tString
	tChar
	tPunct
)

type token struct {
	kind tokKind
	text string
	pos  Pos
	// bol is true when a line break (outside a comment) comes before the
	// token, or the token is the first of the file. A directive starts at a
	// `#` with bol set and ends before the next token with bol set.
	bol bool
}

func (t token) is(text string) bool {
	return t.kind != tEOF && t.kind != tString && t.kind != tChar && t.text == text
}

// puncts lists the C punctuators, longest first (maximal munch). The
// digraphs are refused before this table is consulted.
var puncts = []string{
	"...", "<<=", ">>=",
	"->", "++", "--", "<<", ">>", "<=", ">=", "==", "!=", "&&", "||",
	"*=", "/=", "%=", "+=", "-=", "&=", "^=", "|=", "##",
	"[", "]", "(", ")", "{", "}", ".", "&", "*", "+", "-", "~", "!",
	"/", "%", "<", ">", "^", "|", "?", ":", ";", "=", ",", "#",
}

var digraphs = []string{"%:", "<:", ":>", "<%", "%>"}

// lex splits src into tokens. Comments are dropped. It refuses what would
// make the token stream differ from the one a C compiler sees: trigraphs,
// line continuations, digraphs, and bytes outside ASCII outside comments.
func lex(file, src string, d *diags) []token {
	var toks []token
	line, col := 1, 1
	i := 0
	bol := true
	pos := func() Pos { return Pos{File: file, Line: line, Col: col} }
	advance := func(n int) {
		for k := 0; k < n && i < len(src); k++ {
			if src[i] == '\n' {
				line++
				col = 1
			} else {
				col++
			}
			i++
		}
	}
	// Phases 1 and 2 of translation run before tokens exist; refuse their
	// inputs everywhere, comments included.
	{
		l, c := 1, 1
		for k := 0; k < len(src); k++ {
			switch {
			case src[k] == '?' && k+2 < len(src) && src[k+1] == '?' && strings.IndexByte("=()/'<!>-", src[k+2]) >= 0:
				d.add(Pos{file, l, c}, CodeLexical, "trigraph %q", src[k:k+3])
			case src[k] == '\\' && (k+1 < len(src) && src[k+1] == '\n' || k+2 < len(src) && src[k+1] == '\r' && src[k+2] == '\n'):
				d.add(Pos{file, l, c}, CodeLexical, "line continuation")
			}
			if src[k] == '\n' {
				l++
				c = 1
			} else {
				c++
			}
		}
	}
	for i < len(src) {
		ch := src[i]
		switch {
		case ch == '\n':
			bol = true
			advance(1)
			continue
		case ch == ' ' || ch == '\t' || ch == '\r' || ch == '\f' || ch == '\v':
			advance(1)
			continue
		case ch == '\\' && (strings.HasPrefix(src[i:], "\\\n") || strings.HasPrefix(src[i:], "\\\r\n")):
			// A line continuation, refused above with its own diagnostic.
			advance(1)
			continue
		case strings.HasPrefix(src[i:], "//"):
			for i < len(src) && src[i] != '\n' {
				advance(1)
			}
			continue
		case strings.HasPrefix(src[i:], "/*"):
			p := pos()
			end := strings.Index(src[i+2:], "*/")
			if end < 0 {
				d.add(p, CodeLexical, "unterminated comment")
				advance(len(src) - i)
				continue
			}
			advance(end + 4)
			continue
		}
		p := pos()
		start := i
		var kind tokKind
		switch {
		case isIdentStart(ch):
			for i < len(src) && isIdentPart(src[i]) {
				advance(1)
			}
			kind = tIdent
		case isDigit(ch) || ch == '.' && i+1 < len(src) && isDigit(src[i+1]):
			advance(1)
			for i < len(src) {
				c := src[i]
				if (c == '+' || c == '-') && strings.IndexByte("eEpP", src[i-1]) >= 0 {
					advance(1)
					continue
				}
				if !isIdentPart(c) && c != '.' {
					break
				}
				advance(1)
			}
			kind = tNumber
		case ch == '"' || ch == '\'':
			advance(1)
			closed := false
			for i < len(src) && src[i] != '\n' {
				if src[i] == '\\' && i+1 < len(src) && src[i+1] != '\n' {
					advance(2)
					continue
				}
				if src[i] == ch {
					advance(1)
					closed = true
					break
				}
				advance(1)
			}
			if !closed {
				d.add(p, CodeLexical, "unterminated literal")
				continue
			}
			kind = tString
			if ch == '\'' {
				kind = tChar
			}
		default:
			matched := ""
			for _, dg := range digraphs {
				if strings.HasPrefix(src[i:], dg) {
					d.add(p, CodeLexical, "digraph %q", dg)
					matched = dg
					break
				}
			}
			if matched != "" {
				advance(len(matched))
				continue
			}
			for _, pu := range puncts {
				if strings.HasPrefix(src[i:], pu) {
					matched = pu
					break
				}
			}
			if matched == "" {
				if ch >= 0x80 {
					d.add(p, CodeLexical, "byte 0x%02x outside ASCII", ch)
				} else {
					d.add(p, CodeLexical, "character %q", string(ch))
				}
				advance(1)
				continue
			}
			advance(len(matched))
			kind = tPunct
		}
		toks = append(toks, token{kind: kind, text: src[start:i], pos: p, bol: bol})
		bol = false
	}
	toks = append(toks, token{kind: tEOF, pos: pos(), bol: true})
	return toks
}

func isIdentStart(c byte) bool { return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }
func isIdentPart(c byte) bool  { return isIdentStart(c) || isDigit(c) }
func isDigit(c byte) bool      { return c >= '0' && c <= '9' }
