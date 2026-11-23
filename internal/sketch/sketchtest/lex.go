package sketchtest

import (
	"go/scanner"
	"go/token"
	"strings"
)

// The properties lex sources with go/scanner, independently of the parser
// under test: the Ergo lexical grammar is Go's for every token but three,
// which lexAll handles itself. `..` is one token (and `1..n` is `1`, `..`,
// `n`, never the float `1.`); an interpreted string with `\( … )` is a head,
// the tokens of each interpolated expression and the text after each `)`;
// an unquoted import path or extern target is one token.

type lexeme struct {
	start, end int
	tok        token.Token
	text       string
	// inner marks a token inside an interpolation, or a string segment
	// after one: no comment can stand before it.
	inner bool
}

// lexAll returns the tokens and comments of src and whether it lexed
// without error. Newline semicolons are dropped.
func lexAll(src string) ([]lexeme, bool) {
	l := &lexer{src: src}
	l.run(0, false)
	return l.out, l.errs == 0
}

type lexer struct {
	src  string
	i    int
	out  []lexeme
	errs int
	// paths counts the words after import (and its go) or extern go that
	// lex as one path token each.
	paths int
}

func (l *lexer) emit(start, end int, tok token.Token, inner bool) {
	l.out = append(l.out, lexeme{start, end, tok, l.src[start:end], inner})
	l.i = end
}

// run lexes from l.i until the end, or, inside an interpolation, up to the
// `)` that closes it (depth zero), which it leaves unconsumed.
func (l *lexer) run(depth int, inner bool) {
	src := l.src
	for l.i < len(src) {
		c := src[l.i]
		switch {
		case c == '\n':
			l.paths = 0
			l.i++
			continue
		case c == ' ' || c == '\t' || c == '\r':
			l.i++
			continue
		}
		start := l.i
		switch {
		case inner && c == ')' && depth == 0:
			return
		case inner && c == '(':
			depth++
		case inner && c == ')':
			depth--
		}
		switch {
		case c == '"':
			l.str(start, inner)
			continue
		case l.paths > 0 && pathByte(c) && !(c == '/' && start+1 < len(src) && (src[start+1] == '/' || src[start+1] == '*')):
			end := start
			for end < len(src) && pathByte(src[end]) {
				if src[end] == '/' && end+1 < len(src) && (src[end+1] == '/' || src[end+1] == '*') {
					break
				}
				end++
			}
			word := src[start:end]
			l.emit(start, end, token.IDENT, inner)
			switch {
			case l.paths == 3 && word == "go":
				l.paths = 2
			case l.paths == 3:
				l.paths = 1
			default:
				l.paths--
			}
			continue
		case strings.HasPrefix(src[start:], "..") && !strings.HasPrefix(src[start:], "..."):
			l.emit(start, start+2, token.PERIOD, inner)
			continue
		case c >= '0' && c <= '9':
			end := start
			for end < len(src) && src[end] >= '0' && src[end] <= '9' {
				end++
			}
			if strings.HasPrefix(src[end:], "..") && !strings.HasPrefix(src[end:], "...") {
				l.emit(start, end, token.INT, inner)
				continue
			}
		}
		l.goToken(start, inner)
	}
}

// pathByte reports whether c continues a path word.
func pathByte(c byte) bool {
	return c == '_' || c == '.' || c == '/' || c == '-' || c == '~' || c == '+' ||
		c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80
}

// goToken lexes one token at start with go/scanner.
func (l *lexer) goToken(start int, inner bool) {
	data := []byte(l.src[start:])
	fset := token.NewFileSet()
	file := fset.AddFile("x", -1, len(data))
	var s scanner.Scanner
	s.Init(file, data, func(token.Position, string) { l.errs++ }, scanner.ScanComments)
	pos, tok, lit := s.Scan()
	if tok == token.EOF {
		l.i = len(l.src)
		return
	}
	text := lit
	if text == "" || tok == token.SEMICOLON {
		text = tok.String()
	}
	off := start + file.Offset(pos)
	end := off + len(text)
	if tok == token.COMMENT || tok == token.STRING {
		end = off + rawLen(l.src[off:])
	}
	if end <= off {
		end = off + 1
	}
	switch {
	case tok == token.IMPORT:
		l.paths = 3
	case tok == token.IDENT && lit == "extern":
		l.paths = -1
	case tok == token.GO && l.paths == -1:
		l.paths = 1
	case tok != token.COMMENT:
		l.paths = 0
	}
	l.emit(off, end, tok, inner)
}

// normalize drops the tokens the formatter's decided normalisations remove
// (D4): the leading `|` of a union (a `|` right after `=`), and the
// parentheses around the one expression of a with or match header when
// they hold no brace and no top-level comma.
func normalize(ls []lexeme) []lexeme {
	drop := map[int]bool{}
	prevCode := func(i int) int {
		for j := i - 1; j >= 0; j-- {
			if ls[j].tok != token.COMMENT && ls[j].tok != token.SEMICOLON {
				return j
			}
		}
		return -1
	}
	for i, l := range ls {
		j := prevCode(i)
		if j < 0 {
			continue
		}
		switch {
		case l.tok == token.OR && ls[j].tok == token.ASSIGN:
			drop[i] = true
		case l.tok == token.LPAREN && (ls[j].text == "with" || ls[j].text == "match"):
			depth, comma, brace, end := 0, false, false, -1
			for k := i; k < len(ls) && end < 0; k++ {
				switch ls[k].tok {
				case token.LPAREN, token.LBRACK:
					depth++
				case token.RPAREN, token.RBRACK:
					depth--
					if depth == 0 {
						end = k
					}
				case token.LBRACE:
					brace = true
				case token.COMMA:
					comma = comma || depth == 1
				}
			}
			next := end + 1
			for next < len(ls) && (ls[next].tok == token.COMMENT || ls[next].tok == token.SEMICOLON) {
				next++
			}
			if end > 0 && !comma && !brace && next < len(ls) && ls[next].tok == token.LBRACE {
				drop[i], drop[end] = true, true
			}
		}
	}
	out := ls[:0:0]
	for i, l := range ls {
		if !drop[i] {
			out = append(out, l)
		}
	}
	return out
}

// str lexes an interpreted string at start: one STRING token, or with
// interpolations the head, the tokens of each expression and each segment
// after a `)`.
func (l *lexer) str(start int, inner bool) {
	src := l.src
	seg := start
	segInner := inner
	for i := start + 1; i < len(src); i++ {
		switch src[i] {
		case '\\':
			if i+1 < len(src) && src[i+1] == '(' {
				l.emit(seg, i+2, token.STRING, segInner)
				l.run(0, true)
				if l.i >= len(src) {
					l.errs++
					return
				}
				seg, segInner = l.i, true
				i = l.i
				continue
			}
			i++
		case '\n':
			l.errs++
			l.emit(seg, i, token.STRING, segInner)
			return
		case '"':
			l.emit(seg, i+1, token.STRING, segInner)
			return
		}
	}
	l.errs++
	l.emit(seg, len(src), token.STRING, segInner)
}
