package v1

import (
	"math"
	"strconv"
	"unicode/utf8"
)

// The value appenders of std json's text (docs/spec/stdlib.md, json): each
// takes a buffer and gives it with a value's JSON text added at its end,
// and writes nothing at or before the buffer's length, so the lowering
// treats a call as Go's append (lower/gobody.go, goBodyAppends). They are
// pure: this file imports only the pure packages, and a pure body names no
// other top-level function or variable.

// JSONAppendQuoted appends s quoted: '"', '\\' and the control bytes
// escaped (\n, \r, \t by name, the others as \u00XX), U+2028 and U+2029
// as \u2028 and \u2029 (as encoding/json, so JavaScript reads them), and
// each byte of invalid UTF-8 as the lone surrogate \udcXX of the byte
// (Python's surrogateescape, PEP 383), which Cursor reads back as the
// byte: an Ergo string holds bytes (types.md, Strings 1), so every string
// round-trips, and a valid UTF-8 string is plain JSON text. When html, <,
// > and & are also escaped as \u003c, \u003e and \u0026 (Go's
// encoding/json default, NewHTMLText); else there is no HTML escaping
// (encoding/json v2's default).
//
//ergo:pure
func JSONAppendQuoted(b []byte, s string, html bool) []byte {
	const hex = "0123456789abcdef"
	// The ASCII bytes a string writes as themselves, '1' at a byte's
	// index, 16 to a group: not a control byte, '"' or '\\'; nor <, > and &
	// when html. A lookup, as encoding/json's safeSet.
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
		r, size := utf8.DecodeRuneInString(s[i:])
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

// JSONAppendString appends s quoted with no HTML escaping
// (JSONAppendQuoted), for the runtime's own JSON (rpc, toml).
func JSONAppendString(b []byte, s string) []byte { return JSONAppendQuoted(b, s, false) }

// JSONBytesText is the bytes of b as a string (Text's Written).
//
//ergo:pure
func JSONBytesText(b []byte) string { return string(b) }

// JSONAppendBool appends true or false.
//
//ergo:pure
func JSONAppendBool(b []byte, v bool) []byte {
	if v {
		return append(b, "true"...)
	}
	return append(b, "false"...)
}

// JSONAppendInt appends n's exact digits.
//
//ergo:pure
func JSONAppendInt(b []byte, n int64) []byte { return strconv.AppendInt(b, n, 10) }

// JSONAppendUint appends n's exact digits.
//
//ergo:pure
func JSONAppendUint(b []byte, n uint64) []byte { return strconv.AppendUint(b, n, 10) }

// JSONAppendFloat appends f in the form encoding/json writes (ES6
// Number.prototype.toString: shortest digits, an exponent below 1e-6 and
// from 1e21), and NaN and the infinities as the strings "NaN", "Infinity"
// and "-Infinity", which JSON has no numbers for (protobuf's JSON mapping).
//
//ergo:pure
func JSONAppendFloat(b []byte, f float64) []byte {
	switch {
	case math.IsNaN(f):
		return append(b, `"NaN"`...)
	case math.IsInf(f, 1):
		return append(b, `"Infinity"`...)
	case math.IsInf(f, -1):
		return append(b, `"-Infinity"`...)
	}
	format := byte('f')
	if abs := math.Abs(f); abs != 0 && (abs < 1e-6 || abs >= 1e21) {
		format = 'e'
	}
	b = strconv.AppendFloat(b, f, format, -1, 64)
	if format == 'e' {
		// clean up e-09 to e-9, as encoding/json does
		n := len(b)
		if n >= 4 && b[n-4] == 'e' && b[n-3] == '-' && b[n-2] == '0' {
			b[n-2] = b[n-1]
			b = b[:n-1]
		}
	}
	return b
}
