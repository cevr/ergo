package eval

import (
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// std toml's parse (ORACLE O27). std toml is Ergo over one Go extern,
// runtime/v1.TOMLParse: Go has no standard TOML package, so the evaluator
// has its own TOML 1.0 reader, written from the TOML 1.0 grammar and the
// std's doc comments (toml.ergo; stdlib.md, toml): the document as the
// JSON text json.Open reads, with dates and times as canonical strings,
// nan and inf as json's special-float strings, keys sorted, and a
// failure that names its line, the dotted key being read and the rule.
// TestTOMLAgreesWithRuntime holds it to toml-test's corpus and to
// runtime/v1, value for value and failure for failure.
func init() {
	natives["toml.parse"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		text := args[0].(string)
		if ab := in.charge(fr, at, 1+int64(len(text))); ab != nil {
			return nil, ab
		}
		out, bad := tomlRead(text, args[1].(int64), args[2].(int64))
		if bad != nil {
			return nil, fr.abrupt(ctlFail, at, stdRecord(fn, "Malformed", map[string]Value{"Line": int64(bad.line), "Key": bad.key, "Reason": bad.reason}), "")
		}
		return out, in.allocate(fr, at, int64(len(out)))
	}
}

// tomlBad is a TOML failure: its line (from 1), key and rule.
type tomlBad struct {
	line        int
	key, reason string
}

// tval is a TOML value while the document is read. A table remembers how
// it was made, since TOML forbids redefining it in some ways: by a
// [header] (header), as an inline table (sealed), implicitly or by dotted
// keys (open), and an array made by [[headers]] (tables).
type tval struct {
	kind    byte // 't' table, 'l' list, 's' string, 'r' raw JSON scalar
	raw     string
	keys    map[string]*tval
	list    []*tval
	made    bool // defined: by a header, a dotted key or inline
	header  bool // defined by a [header]
	sealed  bool // an inline table
	headers bool // an array of [[tables]]
}

func newTable(made bool) *tval { return &tval{kind: 't', keys: map[string]*tval{}, made: made} }

func (v *tval) write(b *strings.Builder) {
	switch v.kind {
	case 's':
		b.WriteString(jsonQuote(v.raw, false))
	case 'r':
		b.WriteString(v.raw)
	case 'l':
		b.WriteByte('[')
		for i, e := range v.list {
			if i > 0 {
				b.WriteByte(',')
			}
			e.write(b)
		}
		b.WriteByte(']')
	default:
		names := make([]string, 0, len(v.keys))
		for k := range v.keys {
			names = append(names, k)
		}
		sort.Strings(names)
		b.WriteByte('{')
		for i, k := range names {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(jsonQuote(k, false))
			b.WriteByte(':')
			v.keys[k].write(b)
		}
		b.WriteByte('}')
	}
}

// tomlReader reads one document. bad is its first failure; once set,
// every step does nothing.
type tomlReader struct {
	s     string
	i     int
	line  int
	key   string
	depth int
	bad   *tomlBad
}

func (r *tomlReader) fail(reason string) {
	if r.bad == nil {
		r.bad = &tomlBad{line: r.line, key: r.key, reason: reason}
	}
}

func (r *tomlReader) done() bool { return r.bad != nil || r.i >= len(r.s) }

func (r *tomlReader) cur() byte {
	if r.done() {
		return 0
	}
	return r.s[r.i]
}

func (r *tomlReader) step() byte {
	if r.done() {
		r.fail("unexpected end of document")
		return 0
	}
	c := r.s[r.i]
	r.i++
	if c == '\n' {
		r.line++
	}
	return c
}

func (r *tomlReader) want(c byte) {
	if r.done() || r.s[r.i] != c {
		r.fail("expected " + strconv.QuoteRune(rune(c)))
		return
	}
	r.step()
}

func (r *tomlReader) blanks() {
	for c := r.cur(); c == ' ' || c == '\t'; c = r.cur() {
		r.step()
	}
}

// gap skips blanks and a comment, and newlines too when lines is set.
func (r *tomlReader) gap(lines bool) {
	for !r.done() {
		switch r.s[r.i] {
		case ' ', '\t':
			r.step()
		case '\n':
			if !lines {
				return
			}
			r.step()
		case '#':
			for !r.done() && r.s[r.i] != '\n' {
				if c := r.step(); c < 0x20 && c != '\t' || c == 0x7f {
					r.fail("control character in comment")
				}
			}
			if !lines {
				return
			}
		default:
			return
		}
	}
}

// tomlRead is the document text as JSON, or its failure.
func tomlRead(text string, maxBytes, maxDepth int64) (string, *tomlBad) {
	r := &tomlReader{line: 1, depth: int(maxDepth)}
	if maxBytes > 0 && int64(len(text)) > maxBytes {
		r.fail("document exceeds MaxBytes")
		return "", r.bad
	}
	if !utf8.ValidString(text) {
		// The line of the first invalid byte.
		for k := 0; k < len(text); {
			c, n := utf8.DecodeRuneInString(text[k:])
			if c == utf8.RuneError && n == 1 {
				break
			}
			if c == '\n' {
				r.line++
			}
			k += n
		}
		r.fail("document is not UTF-8")
		return "", r.bad
	}
	if r.depth <= 0 {
		r.depth = 10000
	}
	r.s = strings.ReplaceAll(text, "\r\n", "\n")
	root := newTable(false)
	table := root
	var at []string
	for {
		r.gap(true)
		if r.done() {
			break
		}
		r.key = strings.Join(at, ".")
		if r.cur() == '[' {
			r.key = ""
			r.step()
			many := r.cur() == '['
			if many {
				r.step()
			}
			at = r.path()
			if r.bad != nil {
				break
			}
			r.key = strings.Join(at, ".")
			r.want(']')
			if many {
				r.want(']')
			}
			table = r.header(root, at, many)
		} else {
			r.pair(table, at, 0)
		}
		r.gap(false)
		if !r.done() && r.cur() != '\n' {
			r.fail("expected end of line")
		}
	}
	if r.bad != nil {
		return "", r.bad
	}
	var b strings.Builder
	root.write(&b)
	return b.String(), nil
}

func bareKeyByte(c byte) bool {
	return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || c == '-' || c == '_'
}

// path reads a dotted key.
func (r *tomlReader) path() []string {
	var out []string
	for {
		r.blanks()
		if c := r.cur(); c == '"' || c == '\'' {
			out = append(out, r.str(false))
		} else {
			from := r.i
			for !r.done() && bareKeyByte(r.s[r.i]) {
				r.step()
			}
			if r.i == from {
				r.fail("expected key")
				return nil
			}
			out = append(out, r.s[from:r.i])
		}
		r.blanks()
		if r.cur() != '.' {
			return out
		}
		r.step()
	}
}

// walk follows keys down from t, making open tables; dotted says a
// dotted key of a pair, which may not reach into a [header] table.
func (r *tomlReader) walk(t *tval, keys []string, dotted bool) *tval {
	for _, k := range keys {
		if t.sealed {
			r.fail("cannot extend an inline table")
			return nil
		}
		next, ok := t.keys[k]
		if !ok {
			next = newTable(dotted)
			t.keys[k] = next
		}
		if dotted && (next.header || next.headers) {
			r.fail("cannot extend a header table by dotted key")
			return nil
		}
		if next.kind == 'l' && next.headers {
			next = next.list[len(next.list)-1]
		}
		if next.kind != 't' || next.sealed {
			r.fail("key is already a value")
			return nil
		}
		t = next
	}
	return t
}

// header is the table a [keys] or [[keys]] line opens.
func (r *tomlReader) header(root *tval, keys []string, many bool) *tval {
	if r.bad != nil {
		return nil
	}
	if len(keys) > r.depth {
		r.fail("table exceeds MaxDepth")
		return nil
	}
	parent := r.walk(root, keys[:len(keys)-1], false)
	if r.bad != nil {
		return nil
	}
	last := keys[len(keys)-1]
	t := parent.keys[last]
	if many {
		if t == nil {
			t = &tval{kind: 'l', headers: true}
			parent.keys[last] = t
		}
		if t.kind != 'l' || !t.headers {
			r.fail("key is already a value or table")
			return nil
		}
		e := newTable(true)
		t.list = append(t.list, e)
		return e
	}
	if t == nil {
		t = newTable(false)
		parent.keys[last] = t
	}
	if t.kind != 't' || t.made || t.sealed {
		r.fail("table already defined")
		return nil
	}
	t.made, t.header = true, true
	return t
}

// pair reads key = value into t, under the table path at; depth is the
// nesting of the inline table it is in.
func (r *tomlReader) pair(t *tval, at []string, depth int) {
	outer := r.key
	keys := r.path()
	if r.bad != nil {
		return
	}
	full := append(append([]string{}, at...), keys...)
	r.key = strings.Join(full, ".")
	if depth > 0 && outer != "" {
		r.key = outer + "." + r.key
	}
	if depth+len(full) > r.depth {
		r.fail("key exceeds MaxDepth")
		return
	}
	r.want('=')
	if r.bad != nil {
		return
	}
	r.blanks()
	parent := r.walk(t, keys[:len(keys)-1], true)
	if r.bad != nil {
		return
	}
	last := keys[len(keys)-1]
	if parent.keys[last] != nil {
		r.fail("key already defined")
		return
	}
	parent.keys[last] = r.value(depth + len(full))
}

func (r *tomlReader) value(depth int) *tval {
	if depth > r.depth {
		r.fail("value exceeds MaxDepth")
		return nil
	}
	switch r.cur() {
	case '"', '\'':
		return &tval{kind: 's', raw: r.str(true)}
	case '[':
		r.step()
		l := &tval{kind: 'l'}
		r.gap(true)
		for !r.done() && r.cur() != ']' {
			l.list = append(l.list, r.value(depth+1))
			r.gap(true)
			if r.cur() != ',' {
				break
			}
			r.step()
			r.gap(true)
		}
		r.want(']')
		return l
	case '{':
		r.step()
		t := newTable(true)
		r.blanks()
		for !r.done() && r.cur() != '}' {
			k := r.key
			r.pair(t, nil, depth+1)
			r.key = k
			r.blanks()
			if r.cur() != ',' {
				break
			}
			r.step()
			r.blanks()
			if r.cur() == '}' {
				r.fail("trailing comma in inline table")
				return nil
			}
		}
		r.want('}')
		t.sealed = true
		return t
	}
	return r.scalar()
}

// str reads a basic or literal string, and a multi-line one where multi
// allows it (a value, not a key).
func (r *tomlReader) str(multi bool) string {
	q := r.step()
	long := false
	if multi && r.i+1 < len(r.s) && r.s[r.i] == q && r.s[r.i+1] == q {
		r.step()
		r.step()
		long = true
		if r.cur() == '\n' {
			r.step() // a newline right after the opening is trimmed
		}
	}
	var b strings.Builder
	for !r.done() {
		if r.s[r.i] == q {
			run := 0
			for r.i+run < len(r.s) && r.s[r.i+run] == q {
				run++
			}
			if !long {
				r.step()
				return b.String()
			}
			if run >= 3 {
				// Up to two quotes before the closing three belong to
				// the string.
				n := min(run, 5)
				b.WriteString(strings.Repeat(string(q), n-3))
				r.i += n
				return b.String()
			}
			for ; run > 0; run-- {
				b.WriteByte(r.step())
			}
			continue
		}
		c := r.step()
		if c == '\\' && q == '"' {
			if long {
				if n := r.cur(); n == '\n' || n == ' ' || n == '\t' {
					// A line-ending backslash trims the whitespace after it.
					r.blanks()
					r.want('\n')
					for n := r.cur(); n == ' ' || n == '\t' || n == '\n'; n = r.cur() {
						r.step()
					}
					continue
				}
			}
			e := r.step()
			switch e {
			case 'b':
				b.WriteByte('\b')
			case 't':
				b.WriteByte('\t')
			case 'n':
				b.WriteByte('\n')
			case 'f':
				b.WriteByte('\f')
			case 'r':
				b.WriteByte('\r')
			case '"', '\\':
				b.WriteByte(e)
			case 'u', 'U':
				n := 4
				if e == 'U' {
					n = 8
				}
				if len(r.s)-r.i < n {
					r.fail("incomplete Unicode escape")
					return ""
				}
				hex := r.s[r.i : r.i+n]
				var v uint64
				for k := 0; k < n; k++ {
					d, ok := unhex(hex[k])
					if !ok {
						r.fail("invalid Unicode escape")
						return ""
					}
					v = v<<4 | uint64(d)
				}
				if v > utf8.MaxRune || 0xd800 <= v && v <= 0xdfff {
					r.fail("escape is not a Unicode scalar")
					return ""
				}
				r.i += n
				b.WriteRune(rune(v))
			default:
				r.fail("unknown string escape")
				return ""
			}
			continue
		}
		if c == 0x7f || c < 0x20 && c != '\t' && !(long && c == '\n') {
			if c == '\n' {
				r.line-- // the byte belongs to the line it ended
			}
			r.fail("control character in string")
			return ""
		}
		b.WriteByte(c)
	}
	r.fail("unclosed string")
	return ""
}

func atomEnd(c byte) bool { return strings.IndexByte(" \t\n,]}#", c) >= 0 }

// scalar reads a bare value: a boolean, an integer, a float, a date or a
// time.
func (r *tomlReader) scalar() *tval {
	from := r.i
	for !r.done() && !atomEnd(r.s[r.i]) {
		r.step()
	}
	// A date and a time may be separated by one space.
	if r.cur() == ' ' && r.i-from == 10 && isDate(r.s[from:r.i]) && r.i+1 < len(r.s) && '0' <= r.s[r.i+1] && r.s[r.i+1] <= '9' {
		r.step()
		for !r.done() && !atomEnd(r.s[r.i]) {
			r.step()
		}
	}
	s := r.s[from:r.i]
	switch s {
	case "true", "false":
		return &tval{kind: 'r', raw: s}
	}
	if base, digits, ok := intText(s); ok {
		n, err := strconv.ParseInt(strings.ReplaceAll(digits, "_", ""), base, 64)
		if err != nil {
			r.fail("integer outside int64 range")
			return nil
		}
		return &tval{kind: 'r', raw: strconv.FormatInt(n, 10)}
	}
	switch s {
	case "nan", "+nan", "-nan":
		return &tval{kind: 's', raw: "NaN"}
	case "inf", "+inf":
		return &tval{kind: 's', raw: "Infinity"}
	case "-inf":
		return &tval{kind: 's', raw: "-Infinity"}
	}
	if floatText(s) {
		f, err := strconv.ParseFloat(strings.ReplaceAll(s, "_", ""), 64)
		if err != nil {
			r.fail("float outside float64 range")
			return nil
		}
		out := strconv.FormatFloat(f, 'g', -1, 64)
		if !strings.ContainsAny(out, ".eE") {
			out += ".0"
		}
		return &tval{kind: 'r', raw: out}
	}
	layout := ""
	switch {
	case isDateTime(s):
		s = s[:10] + "T" + s[11:]
		if strings.HasSuffix(s, "z") {
			s = s[:len(s)-1] + "Z"
		}
		layout = "2006-01-02T15:04:05.999999999"
		if zoned := strings.HasSuffix(s, "Z"); zoned || s[len(s)-6] == '+' || s[len(s)-6] == '-' {
			layout = time.RFC3339Nano
			if !zoned {
				if h, m := s[len(s)-5:len(s)-3], s[len(s)-2:]; h > "23" || m > "59" {
					r.fail("invalid UTC offset")
					return nil
				}
			}
		}
	case isTime(s):
		layout = "15:04:05.999999999"
	case isDate(s):
		layout = "2006-01-02"
	}
	if layout == "" {
		r.fail("invalid value")
		return nil
	}
	t, err := time.ParseInLocation(layout, s, time.UTC)
	if err != nil {
		r.fail("invalid date or time")
		return nil
	}
	return &tval{kind: 's', raw: t.Format(layout)}
}

// digitsIn: s is digits of alphabet, an underscore only between two.
func digitsIn(s, alphabet string) bool {
	if s == "" {
		return false
	}
	for k := 0; k < len(s); k++ {
		if s[k] == '_' {
			if k == 0 || k == len(s)-1 || s[k-1] == '_' {
				return false
			}
			continue
		}
		if strings.IndexByte(alphabet, s[k]) < 0 {
			return false
		}
	}
	return true
}

func unsigned(s string) string {
	if s != "" && (s[0] == '+' || s[0] == '-') {
		return s[1:]
	}
	return s
}

const decimalDigits = "0123456789"

// decimalText: digits with no leading zero.
func decimalText(s string) bool { return digitsIn(s, decimalDigits) && (len(s) == 1 || s[0] != '0') }

// intText reads a TOML integer: decimal with an optional sign, or 0x, 0o,
// 0b with no sign.
func intText(s string) (base int, digits string, ok bool) {
	if len(s) > 2 && s[0] == '0' {
		alphabets := map[byte]struct {
			base int
			of   string
		}{'x': {16, "0123456789abcdefABCDEF"}, 'o': {8, "01234567"}, 'b': {2, "01"}}
		if a, prefixed := alphabets[s[1]]; prefixed {
			return a.base, s[2:], digitsIn(s[2:], a.of)
		}
	}
	if len(s) == 2 && s[0] == '0' && strings.IndexByte("xob", s[1]) >= 0 {
		return 0, "", false
	}
	return 10, s, decimalText(unsigned(s))
}

func floatText(s string) bool {
	mant, exp, hasExp := strings.Cut(strings.ToLower(unsigned(s)), "e")
	if hasExp && !digitsIn(unsigned(exp), decimalDigits) {
		return false
	}
	whole, frac, hasFrac := strings.Cut(mant, ".")
	return (hasExp || hasFrac) && decimalText(whole) && (!hasFrac || digitsIn(frac, decimalDigits))
}

func allDigits(s string) bool {
	for k := 0; k < len(s); k++ {
		if s[k] < '0' || s[k] > '9' {
			return false
		}
	}
	return s != ""
}

func isDate(s string) bool {
	return len(s) == 10 && s[4] == '-' && s[7] == '-' && allDigits(s[:4]) && allDigits(s[5:7]) && allDigits(s[8:])
}

func isTime(s string) bool {
	if len(s) < 8 || s[2] != ':' || s[5] != ':' || !allDigits(s[:2]) || !allDigits(s[3:5]) || !allDigits(s[6:8]) {
		return false
	}
	return len(s) == 8 || s[8] == '.' && allDigits(s[9:])
}

func isDateTime(s string) bool {
	if len(s) < 19 || !isDate(s[:10]) || strings.IndexByte("Tt ", s[10]) < 0 {
		return false
	}
	t := s[11:]
	switch {
	case strings.HasSuffix(t, "Z") || strings.HasSuffix(t, "z"):
		t = t[:len(t)-1]
	case len(t) >= 14 && (t[len(t)-6] == '+' || t[len(t)-6] == '-'):
		off := t[len(t)-5:]
		if off[2] != ':' || !allDigits(off[:2]) || !allDigits(off[3:]) {
			return false
		}
		t = t[:len(t)-6]
	}
	return isTime(t)
}
