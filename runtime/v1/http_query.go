package v1

import "strings"

// The query encoding of std http (docs/spec/stdlib.md, http 11): a file of
// pure bodies, so it imports only PureImports.

// HTTPEncodeQuery is std http.EncodeQuery and the query HTTPDo sends for a
// Request's Query: fields in their order, name=value joined by "&", each
// escaped as Go's url.QueryEscape escapes it: the unreserved bytes (RFC
// 3986: letters, digits, "-", "_", ".", "~") as they are, a space as "+",
// and every other byte as "%XX" in upper case. A marked body names no
// other function of the package, so the escape is a local closure.
//
//ergo:pure
//ergo:wait none
func HTTPEncodeQuery(fields []HTTPField) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	escape := func(s string) {
		for i := 0; i < len(s); i++ {
			c := s[i]
			switch {
			case c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.' || c == '~':
				b.WriteByte(c)
			case c == ' ':
				b.WriteByte('+')
			default:
				b.WriteByte('%')
				b.WriteByte(hex[c>>4])
				b.WriteByte(hex[c&15])
			}
		}
	}
	for i, f := range fields {
		if i > 0 {
			b.WriteByte('&')
		}
		escape(f.Name)
		b.WriteByte('=')
		escape(f.Value)
	}
	return b.String()
}
