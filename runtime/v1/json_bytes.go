package v1

import "unicode/utf8"

// The bytes form of std json's string appender (GX10: the prelude Writer's
// StringBytes). It is pure: this file imports only the pure packages, and
// a pure body names no other top-level function or variable, so its body
// is JSONAppendQuoted's (json_append.go) over a []byte.

// JSONAppendQuotedBytes appends the string whose bytes are s quoted, as
// JSONAppendQuoted does (GX10: Writer's StringBytes). A pure body names
// no other function, so it is JSONAppendQuoted's loop over a []byte;
// TestJSONAppendQuotedBytesIsQuoted holds the two to one text.
//
//ergo:pure
//ergo:wait none
func JSONAppendQuotedBytes(b []byte, s []byte, html bool) []byte {
	const hex = "0123456789abcdef"
	const plain = "0000000000000000" + "0000000000000000" + "1101111111111111" + "1111111111111111" +
		"1111111111111111" + "1111111111110111" + "1111111111111111" + "1111111111111111"
	const plainHTML = "0000000000000000" + "0000000000000000" + "1101110111111111" + "1111111111110101" +
		"1111111111111111" + "1111111111110111" + "1111111111111111" + "1111111111111111"
	safe := plain
	if html {
		safe = plainHTML
	}
	b = append(b, '"')
	start := 0
	for i := 0; i < len(s); {
		if c := s[i]; c < utf8.RuneSelf {
			if safe[c] == '1' {
				i++
				continue
			}
			b = append(b, s[start:i]...)
			switch c {
			case '"', '\\':
				b = append(b, '\\', c)
			case '\n':
				b = append(b, '\\', 'n')
			case '\r':
				b = append(b, '\\', 'r')
			case '\t':
				b = append(b, '\\', 't')
			default:
				b = append(b, '\\', 'u', '0', '0', hex[c>>4], hex[c&0xf])
			}
			i++
			start = i
			continue
		}
		r, size := utf8.DecodeRune(s[i:])
		if r == utf8.RuneError && size == 1 {
			b = append(b, s[start:i]...)
			c := s[i]
			b = append(b, '\\', 'u', 'd', 'c', hex[c>>4], hex[c&0xf])
			i += size
			start = i
			continue
		}
		if r == '\u2028' || r == '\u2029' {
			b = append(b, s[start:i]...)
			b = append(b, '\\', 'u', '2', '0', '2', hex[r&0xf])
			i += size
			start = i
			continue
		}
		i += size
	}
	b = append(b, s[start:]...)
	return append(b, '"')
}
