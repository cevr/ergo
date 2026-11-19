package lower

// anchorLines maps every Go line after a //line directive to that directive.
// Go counts the lines that follow a //line directive on from it, so a
// statement go/format spreads over several Go lines (an anonymous struct
// type, a long literal) would name later .ergo lines, and so would the
// lines after an inline site directive (atSite). Each Go line that does not
// directly follow a //line directive gets that directive again, inline,
// before its first token, so a Go line names its own statement and an
// inline directive holds for the rest of its line only (stage C round 2
// review m3). A line inside a raw string or a block comment, a line that
// starts with a comment, and a line that starts with a semicolon are left
// as they are.
//
// It reads the text once, a byte at a time, with only the lexical state Go
// needs to tell a token from a comment or a string's inside: whether a raw
// string or a block comment is open at the line's start. It allocates the
// list of insertions and the result, no token (go/scanner made a string of
// every identifier and literal; anchorLinesScan, the reference in
// anchor_property_test.go, is that pass, and TestAnchorLinesAgree holds the
// two equal).
func anchorLines(src []byte) []byte {
	type insert struct {
		at     int
		cs, ce int // the directive's text in src
	}
	var inserts []insert
	cs, ce, curLine := 0, -1, 0 // ce < 0: no directive yet
	line := 1
	// open is the multi-line token open at i: 0 for none, '`' for a raw
	// string, '*' for a block comment.
	var open byte
	n := len(src)
	i := 0
	for i < n {
		// i is the start of line `line`.
		start := true
		if open != 0 {
			start = false
		}
		for i < n && src[i] != '\n' {
			c := src[i]
			switch open {
			case '`':
				if c == '`' {
					open = 0
				}
				i++
				continue
			case '*':
				if c == '*' && i+1 < n && src[i+1] == '/' {
					open = 0
					i += 2
				} else {
					i++
				}
				continue
			}
			if c == ' ' || c == '\t' || c == '\r' {
				i++
				continue
			}
			first := start
			start = false
			switch {
			case c == '/' && i+1 < n && src[i+1] == '/':
				end := i
				for end < n && src[end] != '\n' {
					end++
				}
				if text := src[i:end]; len(text) >= len("//line ") && string(text[:len("//line ")]) == "//line " {
					cs, ce, curLine = i+len("//line "), end, line
				}
				i = end
				continue
			case c == '/' && i+1 < n && src[i+1] == '*':
				open = '*'
				i += 2
				continue
			}
			if first && c != ';' && ce > cs && line != curLine+1 {
				inserts = append(inserts, insert{i, cs, ce})
			}
			switch c {
			case '`':
				open = '`'
				i++
			case '"', '\'':
				// An interpreted string or a rune: to its closing quote
				// on this line, past escapes.
				i++
				for i < n && src[i] != '\n' {
					if src[i] == '\\' && i+1 < n && src[i+1] != '\n' {
						i += 2
						continue
					}
					if src[i] == c {
						i++
						break
					}
					i++
				}
			default:
				i++
			}
		}
		i++ // the line feed
		line++
	}
	if len(inserts) == 0 {
		return src
	}
	size := len(src)
	for _, in := range inserts {
		size += len("/*line */") + in.ce - in.cs
	}
	// One array of the final size, returned as it is (SL9b: a growing
	// builder and its copy were most of this pass's bytes).
	b := make([]byte, 0, size)
	prev := 0
	for _, in := range inserts {
		b = append(b, src[prev:in.at]...)
		b = append(b, "/*line "...)
		b = append(b, src[in.cs:in.ce]...)
		b = append(b, "*/"...)
		prev = in.at
	}
	return append(b, src[prev:]...)
}
