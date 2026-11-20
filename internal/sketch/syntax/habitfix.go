package syntax

import "strings"

// Fix is the edit that rewrites a Go habit into its Ergo form: the Length
// bytes at Offset become Text. ergo fix applies it (fix.Lets), one habit
// per parse, since the parse stops at its first error.
type Fix struct {
	Offset, Length int
	Text           string
}

// localDecl reports a Go local declaration, `var x [T] [= e]` or `x := e`,
// at the start of a statement, with the let form that replaces it: `let
// mut` when a later statement of the declaring block assigns the name,
// else `let` (no shadowing, so the name is the one variable there). The
// message is the habit's, then that form; its fix rewrites `var` or
// `x :=`. Any other shape returns, and the generic habit is reported where
// the parser meets the token.
func (p *parser) localDecl(h int) {
	var name string
	var at, end, from, to Pos
	var rest int // the token index where the declaration's text after the head starts
	switch h {
	case habitVar:
		v, id := p.tok, p.peek(1)
		if id.tok != IDENT {
			return
		}
		at, end, from, to, name, rest = v.pos, v.end, v.pos, v.end, id.lit, 1
	case habitDefine:
		id, d := p.tok, p.peek(1)
		at, end, from, to, name, rest = d.pos, d.end, id.pos, d.end, id.lit, 2
	}
	stop := p.statementEnd(rest)
	kw := "let"
	if p.assignedAfter(name, stop) {
		kw = "let mut"
	}
	text := kw
	if h == habitDefine {
		text = kw + " " + name + " ="
	}
	shown := text
	if tail := strings.TrimSpace(p.src[p.peek(rest).pos:p.peek(stop).pos]); tail != "" {
		shown += " " + tail
	}
	if r := []rune(shown); len(r) > 48 {
		shown = string(r[:47]) + "…"
	}
	why := name + " is never reassigned"
	if kw != "let" {
		why = name + " is reassigned"
	}
	msg := habits[h].Message + "; here `" + shown + "`, since " + why
	p.errorFix(at, int(end-at), msg, &Fix{Offset: int(from), Length: int(to - from), Text: text})
}

// statementEnd is the index, from the current token, of the token that ends
// the statement starting at index i: a semicolon at its depth, or the brace
// that closes its block.
func (p *parser) statementEnd(i int) int {
	depth := 0
	for ; ; i++ {
		switch t := p.peek(i); t.tok {
		case LBRACE, LPAREN, LBRACK:
			depth++
		case RBRACE, RPAREN, RBRACK:
			if depth == 0 {
				return i
			}
			depth--
		case SEMICOLON:
			if depth == 0 {
				return i
			}
		case EOF:
			return i
		}
	}
}

// assignedAfter reports whether a statement after index i, up to the end of
// the declaring block, assigns name: `name = e`, `name += e`, `name.f = e`
// or `name[k] = e`, in a nested block or function literal too.
func (p *parser) assignedAfter(name string, i int) bool {
	depth := 0
	start := true
	for ; ; i++ {
		t := p.peek(i)
		switch t.tok {
		case EOF:
			return false
		case LBRACE:
			depth++
			start = true
			continue
		case RBRACE:
			if depth == 0 {
				return false
			}
			depth--
			start = false
			continue
		case SEMICOLON, COLON:
			start = true
			continue
		}
		if start && t.tok == IDENT && t.lit == name && p.assigns(i+1) {
			return true
		}
		start = false
	}
}

// assigns reports whether the statement whose first operand ends before
// index i is an assignment: an assignment operator at the statement's
// depth before its end, after a selector or index chain.
func (p *parser) assigns(i int) bool {
	depth := 0
	for ; ; i++ {
		switch t := p.peek(i); t.tok {
		case EOF:
			return false
		case LPAREN, LBRACK:
			depth++
		case RPAREN, RBRACK:
			depth--
		case PERIOD:
		case IDENT:
			if depth == 0 && p.peek(i-1).tok != PERIOD {
				return false
			}
		case ASSIGN, ADD_ASSIGN, SUB_ASSIGN, MUL_ASSIGN, QUO_ASSIGN, REM_ASSIGN,
			AND_ASSIGN, OR_ASSIGN, XOR_ASSIGN, SHL_ASSIGN, SHR_ASSIGN, AND_NOT_ASSIGN:
			return depth == 0
		default:
			if depth == 0 {
				return false
			}
		}
	}
}
