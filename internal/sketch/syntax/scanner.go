package syntax

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// token is one lexical token. lit is the source text of identifiers,
// literals, paths and string segments (carriage returns removed from raw
// strings, as go/scanner does), and ";" or "\n" for an explicit or inserted
// semicolon.
type token struct {
	tok Token
	pos Pos
	end Pos
	lit string
}

// durationUnits are the duration literal suffixes (grammar.md, Literals).
var durationUnits = map[string]bool{"ns": true, "us": true, "ms": true, "s": true, "m": true, "h": true}

// interpolation is an open `\( … )` of an interpreted string: quote is the
// string's opening quote, depth the parentheses open inside the
// interpolation.
type interpolation struct {
	quote int
	depth int
}

// scanner splits a source into tokens with Go's semicolon insertion. It
// collects comments apart from the tokens, as go/scanner with ScanComments
// does for go/parser. The first lexical error ends the token stream with an
// ILLEGAL token and an EOF (see fail), so the parser reports whichever
// error comes first in the source, and every stream ends with EOF.
//
// Two contexts change how words lex. After `import` (and its optional `go`)
// up to two words on the line are PATH tokens, the alias and the unquoted
// import path; after `extern go` one word is the PATH of the target. A path
// word has nonempty elements separated by `/` or `.`; an element starts
// with a letter or `_` and continues with letters, digits, `_` or `-`
// (grammar.md, Literals). Inside an interpreted string, `\(` opens an
// interpolation whose tokens lex normally up to its matching `)`.
type scanner struct {
	src      string
	offset   int
	lines    []int // byte offsets of line starts
	toks     []token
	comments []*Comment
	semi     bool // a newline here inserts a semicolon
	err      *Error

	paths   int  // path words still to lex as PATH on this line
	pathGo  bool // the next path word may be `go` (right after import)
	externs bool // the previous token is extern: `go` then one path word
	goPath  bool // after import go: the path words follow Go's rule, which the checker applies (#231)
	interp  []interpolation
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
	case IDENT, INT, FLOAT, DURATION, STRING, STRING_TAIL, RUNE, PATH, RPAREN, RBRACK, RBRACE, INC, DEC, RETURN, BREAK, CONTINUE, TAKES:
		s.semi = true
	default:
		s.semi = false
	}
	// The word contexts of import and extern.
	switch {
	case tok == IMPORT:
		s.paths, s.pathGo, s.externs, s.goPath = 2, true, false, false
	case tok == EXTERN:
		s.paths, s.pathGo, s.externs, s.goPath = 0, false, true, false
	case tok == GO && s.externs:
		s.paths, s.pathGo, s.externs = 1, false, false
	case tok == GO && s.pathGo:
		s.pathGo, s.goPath = false, true
	case tok == PATH:
		s.paths--
		s.pathGo = false
	default:
		s.paths, s.pathGo, s.externs, s.goPath = 0, false, false, false
	}
}

// insertSemi emits the semicolon a newline at pos implies.
func (s *scanner) insertSemi(pos int) {
	s.paths, s.pathGo, s.externs, s.goPath = 0, false, false, false
	if s.semi {
		s.toks = append(s.toks, token{SEMICOLON, Pos(pos), Pos(pos), "\n"})
		s.semi = false
	}
}

// fail records the first lexical error, at pos, and ends the token stream
// with an ILLEGAL token and an EOF. The error stays where the source is
// wrong; the two tokens keep the stream's invariant, which every lookahead
// of the parser relies on to stop and to slice the source between tokens
// (#226): the stream ends with its one EOF, which peek returns past the
// end, and each token starts at or after the end of the one before it
// (checkStream in the tests). The ILLEGAL token covers the error's span
// unless a token already emitted ends past pos: an open interpolation is
// reported at its string's opening quote, behind the interpolation's
// tokens, and there the ILLEGAL token is empty at the last token's end.
func (s *scanner) fail(pos, length int, format string, args ...any) {
	s.err = newError(s.src, s.lines, Pos(pos), length, fmt.Sprintf(format, args...))
	start, end := Pos(s.err.Offset), Pos(s.err.Offset+s.err.Length)
	if n := len(s.toks); n > 0 && s.toks[n-1].end > start {
		start, end = s.toks[n-1].end, s.toks[n-1].end
	}
	s.toks = append(s.toks, token{ILLEGAL, start, end, ""}, token{EOF, end, end, ""})
}

func (s *scanner) run() {
	src := s.src
	for s.err == nil {
		if s.offset >= len(src) {
			if len(s.interp) > 0 {
				s.fail(s.interp[len(s.interp)-1].quote, 1, "string literal not terminated")
				return
			}
			s.insertSemi(len(src))
			s.toks = append(s.toks, token{EOF, Pos(len(src)), Pos(len(src)), ""})
			return
		}
		start := s.offset
		c := src[start]
		switch {
		case c == '\n':
			if len(s.interp) > 0 {
				s.fail(s.interp[len(s.interp)-1].quote, 1, "newline in string")
				return
			}
			s.insertSemi(start)
			s.offset++
		case c == ' ' || c == '\t' || c == '\r':
			s.offset++
		case c == '/' && start+1 < len(src) && (src[start+1] == '/' || src[start+1] == '*'):
			s.comment(start)
		case s.paths > 0 && pathStart(src, start):
			s.pathWord(start)
		case '0' <= c && c <= '9', c == '.' && start+1 < len(src) && '0' <= src[start+1] && src[start+1] <= '9':
			s.number(start)
		case c == '"':
			s.interpreted(start, start, STRING)
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

func (s *scanner) comment(start int) {
	src := s.src
	if src[start+1] == '/' {
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
		return
	}
	end := strings.Index(src[start+2:], "*/")
	if end < 0 {
		s.fail(start, 2, "block comment not terminated")
		return
	}
	end += start + 4
	raw := src[start:end]
	if strings.IndexByte(raw, '\n') >= 0 {
		// A block comment that spans lines acts as a newline (Go).
		s.insertSemi(start)
	}
	s.comments = append(s.comments, &Comment{Slash: Pos(start), Text: strings.ReplaceAll(raw, "\r", ""), EndPos: Pos(end)})
	s.offset = end
}

// pathStart reports whether a path word starts at i: a letter, a digit or
// `_`.
func pathStart(src string, i int) bool {
	r, _ := utf8.DecodeRuneInString(src[i:])
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

// pathRune reports whether r continues a path word.
func pathRune(r rune) bool {
	switch r {
	case '_', '.', '/', '-', '~', '+':
		return true
	}
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

// pathWord scans an import or extern path, enforcing grammar.md's
// import_path. Comments end a path word just as they end any other token;
// run scans the comment next. Right after import, the word `go` is the
// keyword of a Go import.
func (s *scanner) pathWord(start int) {
	src := s.src
	end := start
	elementStart := true
	for end < len(src) {
		if src[end] == '/' && end+1 < len(src) && (src[end+1] == '/' || src[end+1] == '*') {
			break
		}
		r, size := utf8.DecodeRuneInString(src[end:])
		if r == utf8.RuneError && size == 1 {
			s.fail(end, 1, "invalid UTF-8 byte %#x", src[end])
			return
		}
		if !pathRune(r) {
			break
		}
		if s.goPath {
			// A Go import path: the checker applies Go's rule
			// (module.CheckImportPath; check.GoImportPathError, #231).
			end += size
			elementStart = false
			continue
		}
		if r == '/' || r == '.' {
			if elementStart {
				s.fail(end, size, "an import path must have nonempty elements")
				return
			}
			elementStart = true
		} else {
			if !identRune(r, elementStart) && (elementStart || r != '-') {
				s.fail(end, size, "invalid character %q in import path element", r)
				return
			}
			elementStart = false
		}
		end += size
	}
	if elementStart {
		s.fail(end-1, 1, "an import path must not end with '/' or '.'")
		return
	}
	word := src[start:end]
	s.offset = end
	if word == "go" && s.pathGo {
		s.emit(GO, start, end, word)
		return
	}
	s.emit(PATH, start, end, word)
}

// identRune applies Go's identifier rule to one decoded rune (grammar.md,
// Identifiers): a letter or '_' anywhere, a decimal digit after the first.
func identRune(r rune, first bool) bool {
	return r == '_' || unicode.IsLetter(r) || !first && unicode.IsDigit(r)
}

// identifier scans an identifier, keyword or reserved word. Code points
// are decoded before they are tested, so the bytes of a multi-byte symbol
// never pass as letters; a code point that cannot start or continue a name
// is S001 where the scanner meets it, including one directly after an
// identifier.
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

// digits scans decimal digits and digit separators from i; separator
// placement is checked on the whole literal (badSeparator).
func digits(src string, i int) int {
	for i < len(src) && ('0' <= src[i] && src[i] <= '9' || src[i] == '_') {
		i++
	}
	return i
}

// bases names the integer literal prefixes (grammar.md, Literals 1).
var bases = map[byte]struct {
	base int
	name string
}{'x': {16, "hexadecimal"}, 'X': {16, "hexadecimal"}, 'o': {8, "octal"}, 'O': {8, "octal"}, 'b': {2, "binary"}, 'B': {2, "binary"}}

// digitValue is the value of the digit c, or 99 when c is no digit.
func digitValue(c byte) int {
	switch {
	case '0' <= c && c <= '9':
		return int(c - '0')
	case 'a' <= c && c <= 'f':
		return int(c-'a') + 10
	case 'A' <= c && c <= 'F':
		return int(c-'A') + 10
	}
	return 99
}

// badSeparator is the offset in lit of the first `_` that does not stand
// between two digits of base, or right after a base prefix (Go's rule), or
// -1 when every separator is placed well.
func badSeparator(lit string, base int, prefixed bool) int {
	digit := func(i int) bool { return i >= 0 && i < len(lit) && digitValue(lit[i]) < base }
	for i := 0; i < len(lit); i++ {
		if lit[i] != '_' {
			continue
		}
		if !(digit(i-1) || prefixed && i == 2) || !digit(i+1) {
			return i
		}
	}
	return -1
}

// prefixed scans an integer literal with a base prefix: 0x1F, 0o600,
// 0b1010, with separators (0xFF_FF). A digit outside the base, a literal
// with no digits and a hexadecimal float are S001.
func (s *scanner) prefixed(start int) {
	src := s.src
	b := bases[src[start+1]]
	end := start + 2
	for end < len(src) {
		r, size := utf8.DecodeRuneInString(src[end:])
		if !identRune(r, false) {
			break
		}
		end += size
	}
	lit := src[start:end]
	if b.base == 16 && (strings.ContainsAny(lit[2:], "pP") || end < len(src) && src[end] == '.' && !strings.HasPrefix(src[end:], "..")) {
		s.fail(start, end-start, "hexadecimal float literals are not part of the language; write a decimal float")
		return
	}
	seen := false
	for i := 2; i < len(lit); i++ {
		if lit[i] == '_' {
			continue
		}
		if digitValue(lit[i]) >= b.base {
			r, _ := utf8.DecodeRuneInString(lit[i:])
			s.fail(start, end-start, "invalid digit %q in the %s literal %s", r, b.name, lit)
			return
		}
		seen = true
	}
	if !seen {
		s.fail(start, end-start, "the %s literal %s has no digits", b.name, lit)
		return
	}
	if badSeparator(lit, b.base, true) >= 0 {
		s.fail(start, end-start, "'_' must separate successive digits in %s", lit)
		return
	}
	s.emit(INT, start, end, lit)
	s.offset = end
}

// number scans a decimal integer, a decimal float (Go's forms: 1.5, 1.,
// .5, 1e9, 2.5e-3) or a duration, each with Go's digit separators
// (1_000), and hands an integer with a base prefix (0x, 0o, 0b) to
// prefixed. `1..n` is the integer 1 and the range operator, never the
// float `1.`. Imaginary literals and hexadecimal floats stay errors.
func (s *scanner) number(start int) {
	src := s.src
	if src[start] == '0' && start+1 < len(src) {
		if _, ok := bases[src[start+1]]; ok {
			s.prefixed(start)
			return
		}
	}
	end := digits(src, start)
	float := false
	if end < len(src) && src[end] == '.' && !(end+1 < len(src) && src[end+1] == '.') {
		float = true
		end = digits(src, end+1)
	}
	if end > start && end < len(src) && (src[end] == 'e' || src[end] == 'E') {
		exp := end + 1
		if exp < len(src) && (src[exp] == '+' || src[exp] == '-') {
			exp++
		}
		if after := digits(src, exp); after > exp {
			float = true
			end = after
		}
	}
	if badSeparator(src[start:end], 10, false) >= 0 {
		s.fail(start, end-start, "'_' must separate successive digits in %s", src[start:end])
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
		if float {
			s.emit(FLOAT, start, end, src[start:end])
		} else {
			s.emit(INT, start, end, src[start:end])
		}
		s.offset = end
		return
	}
	suffix := src[end:suffixEnd]
	switch {
	case suffix == "i":
		s.fail(start, suffixEnd-start, "imaginary literals are not part of the language")
	case !float && durationUnits[suffix]:
		s.emit(DURATION, start, suffixEnd, src[start:suffixEnd])
		s.offset = suffixEnd
	default:
		s.fail(start, suffixEnd-start, "unknown number suffix %q", suffix)
	}
}

// interpreted scans an interpreted string from segStart, where a segment
// of kind kind starts: the opening quote of a string (STRING), or the `)`
// that closes an interpolation (STRING_MID). quote is the string's opening
// quote. A `\(` ends the segment as STRING_HEAD or STRING_MID and opens an
// interpolation; the closing quote ends it as STRING or STRING_TAIL.
func (s *scanner) interpreted(quote, segStart int, kind Token) {
	src := s.src
	for i := segStart + 1; i < len(src); i++ {
		switch src[i] {
		case '\\':
			if i+1 < len(src) && src[i+1] == '(' {
				head := STRING_HEAD
				if kind != STRING {
					head = STRING_MID
				}
				s.emit(head, segStart, i+2, src[segStart:i+2])
				s.interp = append(s.interp, interpolation{quote: quote})
				s.offset = i + 2
				return
			}
			i++
			if i < len(src) && src[i] == '\n' {
				s.fail(quote, 1, "newline in string")
				return
			}
		case '\n':
			s.fail(quote, 1, "newline in string")
			return
		case '"':
			tail := STRING
			if kind != STRING {
				tail = STRING_TAIL
			}
			s.emit(tail, segStart, i+1, src[segStart:i+1])
			s.offset = i + 1
			return
		}
	}
	s.fail(quote, 1, "string literal not terminated")
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

// operators are Go's operators and punctuation plus `..`, longest first.
// The removed operators (:=, <-, ++, --, ...) still lex, so `x <-y` is
// never `x < -y` and the parser can name the Ergo form.
var operators = []struct {
	text string
	tok  Token
}{
	{"...", ELLIPSIS}, {"<<=", SHL_ASSIGN}, {">>=", SHR_ASSIGN}, {"&^=", AND_NOT_ASSIGN},
	{"&^", AND_NOT}, {"<<", SHL}, {">>", SHR}, {"+=", ADD_ASSIGN}, {"-=", SUB_ASSIGN},
	{"*=", MUL_ASSIGN}, {"/=", QUO_ASSIGN}, {"%=", REM_ASSIGN}, {"&=", AND_ASSIGN},
	{"|=", OR_ASSIGN}, {"^=", XOR_ASSIGN}, {"&&", LAND}, {"||", LOR}, {"<-", ARROW},
	{"++", INC}, {"--", DEC}, {"==", EQL}, {"!=", NEQ}, {"<=", LEQ}, {">=", GEQ},
	{":=", DEFINE}, {"..", DOTDOT},
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
	if n := len(s.interp); n > 0 {
		switch tok {
		case LPAREN:
			s.interp[n-1].depth++
		case RPAREN:
			if s.interp[n-1].depth == 0 {
				// The `)` closes the interpolation: the string goes on.
				frame := s.interp[n-1]
				s.interp = s.interp[:n-1]
				s.interpreted(frame.quote, start, STRING_MID)
				return
			}
			s.interp[n-1].depth--
		}
	}
	s.emit(tok, start, start+1, rest[:1])
	s.offset = start + 1
}
