package oldsyntax

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// token is one lexical token. lit is the source text of identifiers and
// literals (carriage returns removed from raw strings, as go/scanner does),
// and ";" or "\n" for an explicit or inserted semicolon.
type token struct {
	tok Token
	pos Pos
	end Pos
	lit string
}

// durationUnits are the duration literal suffixes (grammar.md, Literals 2).
var durationUnits = map[string]bool{"ns": true, "us": true, "ms": true, "s": true, "m": true, "h": true}

// scanner splits a source into tokens with Go's semicolon insertion. It
// collects comments apart from the tokens, as go/scanner with ScanComments
// does for go/parser. The first lexical error ends the token stream with an
// ILLEGAL token at the error's position, so the parser reports whichever
// error comes first in the source.
type scanner struct {
	src      string
	offset   int
	lines    []int // byte offsets of line starts
	toks     []token
	comments []*Comment
	semi     bool // a newline here inserts a semicolon
	err      *Error
}

func scan(src string) *scanner {
	s := &scanner{src: src, lines: []int{0}}
	for i := 0; i < len(src); i++ {
		if src[i] == '\n' {
			s.lines = append(s.lines, i+1)
		}
	}
	s.run()
	return s
}

func (s *scanner) emit(tok Token, pos, end int, lit string) {
	s.toks = append(s.toks, token{tok, Pos(pos), Pos(end), lit})
	switch tok {
	case IDENT, INT, DURATION, STRING, RUNE, RPAREN, RBRACK, RBRACE, INC, DEC, RETURN, BREAK, CONTINUE:
		s.semi = true
	default:
		s.semi = false
	}
}

// insertSemi emits the semicolon a newline at pos implies.
func (s *scanner) insertSemi(pos int) {
	if s.semi {
		s.toks = append(s.toks, token{SEMICOLON, Pos(pos), Pos(pos), "\n"})
		s.semi = false
	}
}

func (s *scanner) fail(pos, length int, format string, args ...any) {
	s.err = newError(s.src, s.lines, Pos(pos), length, fmt.Sprintf(format, args...))
	s.toks = append(s.toks, token{ILLEGAL, Pos(pos), Pos(pos + length), ""})
}

func (s *scanner) run() {
	src := s.src
	for s.err == nil {
		if s.offset >= len(src) {
			s.insertSemi(len(src))
			s.toks = append(s.toks, token{EOF, Pos(len(src)), Pos(len(src)), ""})
			return
		}
		start := s.offset
		c := src[start]
		switch {
		case c == '\n':
			s.insertSemi(start)
			s.offset++
		case c == ' ' || c == '\t' || c == '\r':
			s.offset++
		case c == '/' && start+1 < len(src) && src[start+1] == '/':
			end := strings.IndexByte(src[start:], '\n')
			if end < 0 {
				end = len(src)
			} else {
				end += start
			}
			s.insertSemi(start)
			text := strings.TrimRight(src[start:end], "\r")
			s.comments = append(s.comments, &Comment{Slash: Pos(start), Text: text, EndPos: Pos(start + len(text))})
			s.offset = end
		case c == '/' && start+1 < len(src) && src[start+1] == '*':
			end := strings.Index(src[start+2:], "*/")
			if end < 0 {
				s.fail(start, 2, "block comment not terminated")
				continue
			}
			end += start + 4
			raw := src[start:end]
			if strings.IndexByte(raw, '\n') >= 0 {
				// A block comment that spans lines acts as a newline (Go).
				s.insertSemi(start)
			}
			s.comments = append(s.comments, &Comment{Slash: Pos(start), Text: strings.ReplaceAll(raw, "\r", ""), EndPos: Pos(end)})
			s.offset = end
		case '0' <= c && c <= '9':
			s.number(start)
		case c == '"':
			s.interpreted(start)
		case c == '`':
			end := strings.IndexByte(src[start+1:], '`')
			if end < 0 {
				s.fail(start, 1, "raw string literal not terminated")
				continue
			}
			end += start + 2
			s.emit(STRING, start, end, strings.ReplaceAll(src[start:end], "\r", ""))
			s.offset = end
		case c == '\'':
			s.runeLit(start)
		case c == '_' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || c >= utf8.RuneSelf:
			s.identifier(start)
		default:
			s.operator(start)
		}
	}
}

// identRune applies Go's identifier rule to one decoded rune (grammar.md,
// Identifiers): a letter or '_' anywhere, a decimal digit after the first.
func identRune(r rune, first bool) bool {
	return r == '_' || unicode.IsLetter(r) || !first && unicode.IsDigit(r)
}

// identifier scans an identifier or keyword. Code points are decoded before
// they are tested, so the bytes of a multi-byte symbol never pass as
// letters; a code point that cannot start or continue a name is S001 where
// the scanner meets it, including one directly after an identifier.
func (s *scanner) identifier(start int) {
	src := s.src
	r, size := utf8.DecodeRuneInString(src[start:])
	if r == utf8.RuneError && size == 1 {
		s.fail(start, 1, "invalid UTF-8 byte %#x", src[start])
		return
	}
	if !identRune(r, true) {
		s.fail(start, size, "unexpected character %U %q", r, r)
		return
	}
	end := start + size
	for end < len(src) {
		r, size := utf8.DecodeRuneInString(src[end:])
		if !identRune(r, false) {
			break
		}
		end += size
	}
	word := src[start:end]
	if tok, ok := keywords[word]; ok {
		s.emit(tok, start, end, word)
	} else {
		s.emit(IDENT, start, end, word)
	}
	s.offset = end
}

// number scans a decimal integer or a duration. The numeric forms of Go numeric types
// (hexadecimal, octal, binary, separators, floating point) stay errors.
func (s *scanner) number(start int) {
	src := s.src
	end := start
	for end < len(src) && '0' <= src[end] && src[end] <= '9' {
		end++
	}
	if end < len(src) && src[end] == '.' {
		// Go reads `1.5`, and also `0.` before any other character, as a
		// floating-point literal: `0.A` is the float `0.` and the name `A`,
		// never a selector on 0 (write `0 .A`, as gofmt does).
		length := end + 1 - start
		if end+1 < len(src) && '0' <= src[end+1] && src[end+1] <= '9' {
			length++
		}
		s.fail(start, length, "floating-point literals are not part of v0")
		return
	}
	suffixEnd := end
	for suffixEnd < len(src) {
		r, size := utf8.DecodeRuneInString(src[suffixEnd:])
		if !identRune(r, false) {
			break
		}
		suffixEnd += size
	}
	if suffixEnd == end {
		s.emit(INT, start, end, src[start:end])
		s.offset = end
		return
	}
	suffix := src[end:suffixEnd]
	if durationUnits[suffix] {
		s.emit(DURATION, start, suffixEnd, src[start:suffixEnd])
		s.offset = suffixEnd
		return
	}
	switch {
	case src[start:end] == "0" && strings.ContainsAny(suffix[:1], "xXbBoO"):
		s.fail(start, suffixEnd-start, "hexadecimal, octal and binary literals are not part of v0")
	case suffix[0] == '_':
		s.fail(start, suffixEnd-start, "digit separators are not part of v0")
	default:
		s.fail(start, suffixEnd-start, "unknown number suffix %q", suffix)
	}
}

func (s *scanner) interpreted(start int) {
	src := s.src
	for i := start + 1; i < len(src); i++ {
		switch src[i] {
		case '\\':
			i++
			if i < len(src) && src[i] == '\n' {
				s.fail(start, 1, "newline in string")
				return
			}
		case '\n':
			s.fail(start, 1, "newline in string")
			return
		case '"':
			s.emit(STRING, start, i+1, src[start:i+1])
			s.offset = i + 1
			return
		}
	}
	s.fail(start, 1, "string literal not terminated")
}

func (s *scanner) runeLit(start int) {
	src := s.src
	for i := start + 1; i < len(src); i++ {
		switch src[i] {
		case '\\':
			i++
			if i < len(src) && src[i] == '\n' {
				s.fail(start, 1, "rune literal not terminated")
				return
			}
		case '\n':
			s.fail(start, 1, "rune literal not terminated")
			return
		case '\'':
			lit := src[start : i+1]
			value, err := strconv.Unquote(lit)
			if err != nil || utf8.RuneCountInString(value) != 1 {
				s.fail(start, i+1-start, "invalid rune literal %s", lit)
				return
			}
			s.emit(RUNE, start, i+1, lit)
			s.offset = i + 1
			return
		}
	}
	s.fail(start, 1, "rune literal not terminated")
}

// operators are Go's operators and punctuation, longest first.
var operators = []struct {
	text string
	tok  Token
}{
	{"...", ELLIPSIS}, {"<<=", SHL_ASSIGN}, {">>=", SHR_ASSIGN}, {"&^=", AND_NOT_ASSIGN},
	{"&^", AND_NOT}, {"<<", SHL}, {">>", SHR}, {"+=", ADD_ASSIGN}, {"-=", SUB_ASSIGN},
	{"*=", MUL_ASSIGN}, {"/=", QUO_ASSIGN}, {"%=", REM_ASSIGN}, {"&=", AND_ASSIGN},
	{"|=", OR_ASSIGN}, {"^=", XOR_ASSIGN}, {"&&", LAND}, {"||", LOR}, {"<-", ARROW},
	{"++", INC}, {"--", DEC}, {"==", EQL}, {"!=", NEQ}, {"<=", LEQ}, {">=", GEQ},
	{":=", DEFINE},
}

var singleOperators = map[byte]Token{
	'+': ADD, '-': SUB, '*': MUL, '/': QUO, '%': REM, '&': AND, '|': OR, '^': XOR,
	'<': LSS, '>': GTR, '=': ASSIGN, '!': NOT, '(': LPAREN, '[': LBRACK, '{': LBRACE,
	',': COMMA, '.': PERIOD, ')': RPAREN, ']': RBRACK, '}': RBRACE, ';': SEMICOLON, ':': COLON,
}

func (s *scanner) operator(start int) {
	rest := s.src[start:]
	for _, op := range operators {
		if strings.HasPrefix(rest, op.text) {
			s.emit(op.tok, start, start+len(op.text), op.text)
			s.offset = start + len(op.text)
			return
		}
	}
	tok, ok := singleOperators[rest[0]]
	if !ok {
		s.fail(start, 1, "unexpected character %q", rest[0])
		return
	}
	s.emit(tok, start, start+1, rest[:1])
	s.offset = start + 1
}
