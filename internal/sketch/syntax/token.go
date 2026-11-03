package syntax

// Pos is a byte offset into a File's source. NoPos marks an absent
// position, such as the parentheses of a match subject that has none.
type Pos int

// NoPos is the zero value of an absent position.
const NoPos Pos = -1

// IsValid reports whether p is a position in the source.
func (p Pos) IsValid() bool { return p >= 0 }

// Token is the kind of a lexical token.
type Token int

// The tokens. Keywords are the 27 of the decided grammar
// (docs/spec/grammar.md, Keywords) plus `as` (#196, ninth round). The 15 Go
// keywords Ergo does not use are reserved: they lex as their own tokens, so
// no name can use them and the parser names the Ergo form for each
// (removedForms). replace, initial and step are contextual and lex as IDENT.
const (
	ILLEGAL Token = iota
	EOF
	COMMENT

	IDENT
	INT      // 42
	FLOAT    // 2.5
	DURATION // 500ms
	STRING   // "a" or `a`
	RUNE     // 'a'
	PATH     // an unquoted import path or extern target: net/http, strings.ToUpper

	// The segments of an interpolated string "a \(x) b \(y) c": the head
	// runs from the opening quote through the first `\(`, a middle from a
	// `)` through the next `\(`, and the tail from the last `)` through the
	// closing quote. The expressions between them are ordinary tokens.
	STRING_HEAD
	STRING_MID
	STRING_TAIL

	ADD // +
	SUB // -
	MUL // *
	QUO // /
	REM // %

	AND     // &
	OR      // |
	XOR     // ^
	SHL     // <<
	SHR     // >>
	AND_NOT // &^

	ADD_ASSIGN     // +=
	SUB_ASSIGN     // -=
	MUL_ASSIGN     // *=
	QUO_ASSIGN     // /=
	REM_ASSIGN     // %=
	AND_ASSIGN     // &=
	OR_ASSIGN      // |=
	XOR_ASSIGN     // ^=
	SHL_ASSIGN     // <<=
	SHR_ASSIGN     // >>=
	AND_NOT_ASSIGN // &^=

	LAND   // &&
	LOR    // ||
	ARROW  // <- (removed; still a token)
	INC    // ++ (removed; still a token)
	DEC    // -- (removed; still a token)
	DOTDOT // ..

	EQL    // ==
	LSS    // <
	GTR    // >
	ASSIGN // =
	NOT    // !

	NEQ      // !=
	LEQ      // <=
	GEQ      // >=
	DEFINE   // := (removed; still a token)
	ELLIPSIS // ... (removed; still a token)

	LPAREN    // (
	LBRACK    // [
	LBRACE    // {
	COMMA     // ,
	PERIOD    // .
	RPAREN    // )
	RBRACK    // ]
	RBRACE    // }
	SEMICOLON // ;
	COLON     // :

	keywordBeg
	BREAK
	CASE
	CONTINUE
	ELSE
	FOR
	FUNC
	IF
	IMPORT
	RETURN
	TYPE
	TRY
	CATCH
	NEEDS
	PROVIDES
	CONTEXT
	MACHINE
	LET
	MUT
	IN
	MATCH
	ESCAPING
	FAILS
	TEST
	LAW
	ASSERT
	EXTERN
	WITH
	AS
	keywordEnd

	reservedBeg
	CHAN
	CONST
	DEFAULT
	DEFER
	FALLTHROUGH
	GO
	GOTO
	INTERFACE
	MAP
	PACKAGE
	RANGE
	SELECT
	STRUCT
	SWITCH
	VAR
	reservedEnd
)

var tokens = [...]string{
	ILLEGAL:     "ILLEGAL",
	EOF:         "EOF",
	COMMENT:     "COMMENT",
	IDENT:       "IDENT",
	INT:         "INT",
	FLOAT:       "FLOAT",
	DURATION:    "DURATION",
	STRING:      "STRING",
	RUNE:        "RUNE",
	PATH:        "PATH",
	STRING_HEAD: "STRING_HEAD",
	STRING_MID:  "STRING_MID",
	STRING_TAIL: "STRING_TAIL",

	ADD: "+", SUB: "-", MUL: "*", QUO: "/", REM: "%",
	AND: "&", OR: "|", XOR: "^", SHL: "<<", SHR: ">>", AND_NOT: "&^",

	ADD_ASSIGN: "+=", SUB_ASSIGN: "-=", MUL_ASSIGN: "*=", QUO_ASSIGN: "/=", REM_ASSIGN: "%=",
	AND_ASSIGN: "&=", OR_ASSIGN: "|=", XOR_ASSIGN: "^=", SHL_ASSIGN: "<<=", SHR_ASSIGN: ">>=", AND_NOT_ASSIGN: "&^=",

	LAND: "&&", LOR: "||", ARROW: "<-", INC: "++", DEC: "--", DOTDOT: "..",
	EQL: "==", LSS: "<", GTR: ">", ASSIGN: "=", NOT: "!",
	NEQ: "!=", LEQ: "<=", GEQ: ">=", DEFINE: ":=", ELLIPSIS: "...",

	LPAREN: "(", LBRACK: "[", LBRACE: "{", COMMA: ",", PERIOD: ".",
	RPAREN: ")", RBRACK: "]", RBRACE: "}", SEMICOLON: ";", COLON: ":",

	BREAK: "break", CASE: "case", CONTINUE: "continue", ELSE: "else", FOR: "for",
	FUNC: "func", IF: "if", IMPORT: "import", RETURN: "return", TYPE: "type",
	TRY: "try", CATCH: "catch", NEEDS: "needs", PROVIDES: "provides", CONTEXT: "wire",
	MACHINE: "machine", LET: "let", MUT: "mut", IN: "in", MATCH: "match",
	ESCAPING: "escaping", FAILS: "fails", TEST: "test", LAW: "law", ASSERT: "assert",
	EXTERN: "extern", WITH: "with", AS: "as",

	CHAN: "chan", CONST: "const", DEFAULT: "default", DEFER: "defer", FALLTHROUGH: "fallthrough",
	GO: "go", GOTO: "goto", INTERFACE: "interface", MAP: "map", PACKAGE: "package",
	RANGE: "range", SELECT: "select", STRUCT: "struct", SWITCH: "switch", VAR: "var",
}

// String is the token's spelling for operators, keywords and reserved
// words, and its class name otherwise.
func (t Token) String() string {
	if 0 <= t && int(t) < len(tokens) && tokens[t] != "" {
		return tokens[t]
	}
	return "token(?)"
}

// IsKeyword reports whether t is one of the language's keywords: the 27 of
// the decided grammar and `as`.
func (t Token) IsKeyword() bool { return keywordBeg < t && t < keywordEnd }

// IsReserved reports whether t is one of the 15 Go keywords Ergo does not
// use. They are never names; go is also the contextual word of `import go`,
// `extern go` and the qualified names go.Error and go.Foreign.
func (t Token) IsReserved() bool { return reservedBeg < t && t < reservedEnd }

// IsLiteral reports whether t is an identifier or a basic literal.
func (t Token) IsLiteral() bool { return IDENT <= t && t <= RUNE }

var keywords = func() map[string]Token {
	m := map[string]Token{}
	for t := keywordBeg + 1; t < keywordEnd; t++ {
		m[tokens[t]] = t
	}
	for t := reservedBeg + 1; t < reservedEnd; t++ {
		m[tokens[t]] = t
	}
	return m
}()

// IsKeyword reports whether name is a keyword or a reserved word: no
// declaration may use it.
func IsKeyword(name string) bool {
	_, ok := keywords[name]
	return ok
}

// Keywords lists the keywords in token order: the 27 of the decided
// grammar, then `as`.
func Keywords() []string {
	var out []string
	for t := keywordBeg + 1; t < keywordEnd; t++ {
		out = append(out, tokens[t])
	}
	return out
}

// Reserved lists the 15 reserved Go keywords in token order.
func Reserved() []string {
	var out []string
	for t := reservedBeg + 1; t < reservedEnd; t++ {
		out = append(out, tokens[t])
	}
	return out
}

// Precedence is the binary precedence of op, or 0 when op is not a binary
// operator: Go's five levels. The range operator `..` binds looser than all
// of them and does not chain (parseRange), as in Rust.
func (op Token) Precedence() int {
	switch op {
	case LOR:
		return 1
	case LAND:
		return 2
	case EQL, NEQ, LSS, LEQ, GTR, GEQ:
		return 3
	case ADD, SUB, OR, XOR:
		return 4
	case MUL, QUO, REM, SHL, SHR, AND, AND_NOT:
		return 5
	}
	return 0
}
