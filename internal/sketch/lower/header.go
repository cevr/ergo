package lower

import "ergo.local/prototype/internal/sketch/syntax"

// Header literals (lw-header-literal, G-ex-c-2). Go parses a composite
// literal of the TypeName form (`T{…}`, `pkg.T{…}`, `T[int]{…}`) between
// the keyword and the block of an if, for or switch as the block itself,
// unless parentheses enclose it (Go spec, Composite literals). The Ergo
// checker asks for the same parentheses (S001), but the lowering drops a
// source ParenExpr, and go/format strips the parentheses that binary writes
// round a whole condition when the literal's type is a generic instance
// (go/printer stripParens knows only Ident and SelectorExpr type names).
// So inside a header every value of that form is written in parentheses of
// its own, which go/format keeps; Go's own spelling `if v != (T{…}) {`.

// headerValue lowers x, an if or for condition, a for-in domain or a match
// subject, so that the Go text parses in a statement header.
func (fc *funcCtx) headerValue(x syntax.Expr) string {
	fc.header++
	defer func() { fc.header-- }()
	v := fc.value(x)
	if len(fc.l.info.Releases[x]) > 0 {
		// The header's temporaries end before its statement's body runs
		// (R43): the value first, then their releases.
		v = fc.spill(v)
		fc.releaseAt(x)
	}
	return v
}

// guardHeader puts val in parentheses when a header is being lowered and
// val is a composite literal of the TypeName form.
func (fc *funcCtx) guardHeader(val string) string {
	if fc.header == 0 || !typeNameLiteral(val) {
		return val
	}
	return "(" + val + ")"
}

// typeNameLiteral reports whether s, a Go expression the lowering wrote, is
// one composite literal whose type is a type name: an identifier, a
// qualified identifier, either with type arguments in brackets, then a
// brace-enclosed body that ends the text. A slice, map, struct or function
// type does not start with an identifier, so it never matches.
func typeNameLiteral(s string) bool {
	i := 0
	ident := func() bool {
		start := i
		for i < len(s) && (s[i] == '_' || s[i] >= 'a' && s[i] <= 'z' || s[i] >= 'A' && s[i] <= 'Z' || s[i] >= '0' && s[i] <= '9' || s[i] >= 0x80) {
			i++
		}
		return i > start && !(s[start] >= '0' && s[start] <= '9')
	}
	if !ident() {
		return false
	}
	switch s[:i] {
	case "map", "struct", "func", "interface", "chan":
		return false
	}
	if i < len(s) && s[i] == '.' {
		i++
		if !ident() {
			return false
		}
	}
	if i < len(s) && s[i] == '[' {
		end := closing(s, i)
		if end < 0 {
			return false
		}
		i = end + 1
	}
	if i >= len(s) || s[i] != '{' {
		return false
	}
	return closing(s, i) == len(s)-1
}

// closing is the index of the bracket that closes the one at s[open], with
// Go string, rune and raw string literals skipped, or -1.
func closing(s string, open int) int {
	depth := 0
	for i := open; i < len(s); i++ {
		switch c := s[i]; c {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
			if depth == 0 {
				return i
			}
		case '"', '\'':
			for i++; i < len(s) && s[i] != c; i++ {
				if s[i] == '\\' {
					i++
				}
			}
		case '`':
			for i++; i < len(s) && s[i] != '`'; i++ {
			}
		}
	}
	return -1
}
