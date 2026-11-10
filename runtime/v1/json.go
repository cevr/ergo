package v1

import (
	"math"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"ergo.local/prototype/runtime/rt"
)

// The bodies of std json (docs/spec/stdlib.md, json; S-json). Text is the
// prelude Writer that writes JSON text into a byte buffer; Cursor reads a
// text JSONOpen has validated whole (phase 1), so its reads never meet a
// syntax error. The lowering's fast path (lower/wirejson.go) appends to a
// Text's buffer directly with the Append functions below, so the two paths
// write the same bytes by construction of these few functions.

// JSONMalformed is std json's Malformed: a text that is not one JSON value,
// or breaks a limit. Offset is the byte offset of the first byte that
// breaks it.
type JSONMalformed struct {
	Offset int
	Reason string
}

// Text is the JSON text written so far. Comma says that the next value or
// key needs a comma before it: true after a value, false after a key and
// after an opening bracket. One flag is the whole state, since a Key ends
// every object member's comma question and a value every list element's.
// The Writer's operations take the caller's task, which they do not use,
// so *Text has the method set of the prelude Writer's Go interface.
type Text struct {
	B     []byte
	Comma bool
	// HTML escapes <, > and & in strings as \u003c, \u003e and \u0026, as
	// Go's encoding/json does by default, so the text is safe inside HTML.
	HTML bool
}

// NewJSONText is a Text with nothing written.
func NewJSONText() *Text { return &Text{B: make([]byte, 0, 64)} }

// NewJSONHTMLText is a Text with nothing written whose strings escape <,
// > and &.
func NewJSONHTMLText() *Text { return &Text{B: make([]byte, 0, 64), HTML: true} }

// JSONAppendTextString appends s quoted to t as t's String does: the fast
// path's string, so both paths honour t.HTML.
func JSONAppendTextString(t *Text, s string) {
	if t.HTML {
		t.B = jsonAppendString(t.B, s, &jsonSafeHTML)
		return
	}
	t.B = jsonAppendString(t.B, s, &jsonSafe)
}

// Null writes null, a value no derived codec writes (a Text's own event,
// for a hand-written text).
func (t *Text) Null() {
	t.JSONValue()
	t.B = append(t.B, "null"...)
	t.Comma = true
}

// JSONValue starts a value: a comma when one is due. Then the value is
// written and JSONDone marks it written.
func (t *Text) JSONValue() {
	if t.Comma {
		t.B = append(t.B, ',')
	}
}

func (t *Text) Bool(task *rt.Task, b bool) {
	t.JSONValue()
	t.B = JSONAppendBool(t.B, b)
	t.Comma = true
}

func (t *Text) Int(task *rt.Task, n int64) {
	t.JSONValue()
	t.B = JSONAppendInt(t.B, n)
	t.Comma = true
}

func (t *Text) Uint(task *rt.Task, n uint64) {
	t.JSONValue()
	t.B = JSONAppendUint(t.B, n)
	t.Comma = true
}

// JSONAppendBool appends true or false.
func JSONAppendBool(b []byte, v bool) []byte {
	if v {
		return append(b, "true"...)
	}
	return append(b, "false"...)
}

// JSONAppendInt appends n's exact digits.
func JSONAppendInt(b []byte, n int64) []byte { return strconv.AppendInt(b, n, 10) }

// JSONAppendUint appends n's exact digits.
func JSONAppendUint(b []byte, n uint64) []byte { return strconv.AppendUint(b, n, 10) }

func (t *Text) Float(task *rt.Task, f float64) {
	t.JSONValue()
	t.B = JSONAppendFloat(t.B, f)
	t.Comma = true
}

func (t *Text) String(task *rt.Task, s string) {
	t.JSONValue()
	JSONAppendTextString(t, s)
	t.Comma = true
}

func (t *Text) BeginList(task *rt.Task, n int) {
	t.JSONValue()
	t.B = append(t.B, '[')
	t.Comma = false
}

func (t *Text) EndList(task *rt.Task) {
	t.B = append(t.B, ']')
	t.Comma = true
}

func (t *Text) BeginObject(task *rt.Task, n int) {
	t.JSONValue()
	t.B = append(t.B, '{')
	t.Comma = false
}

func (t *Text) EndObject(task *rt.Task) {
	t.B = append(t.B, '}')
	t.Comma = true
}

// Tag writes a union's tag as the member "_tag" (owner q81).
func (t *Text) Tag(task *rt.Task, name string) {
	t.Key(task, "_tag")
	t.String(task, name)
}

func (t *Text) Key(task *rt.Task, k string) {
	t.JSONValue()
	JSONAppendTextString(t, k)
	t.B = append(t.B, ':')
	t.Comma = false
}

// Written is the text written so far.
func (t *Text) Written() string { return string(t.B) }

// JSONAppendFloat appends f in the form encoding/json writes (ES6
// Number.prototype.toString: shortest digits, an exponent below 1e-6 and
// from 1e21), and NaN and the infinities as the strings "NaN", "Infinity"
// and "-Infinity", which JSON has no numbers for (protobuf's JSON mapping).
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

// jsonSafe holds the ASCII bytes a JSON string writes as themselves.
var jsonSafe = func() (t [utf8.RuneSelf]bool) {
	for c := 0x20; c < utf8.RuneSelf; c++ {
		t[c] = c != '"' && c != '\\'
	}
	return t
}()

// jsonSafeHTML is jsonSafe without <, > and &.
var jsonSafeHTML = func() (t [utf8.RuneSelf]bool) {
	t = jsonSafe
	t['<'], t['>'], t['&'] = false, false, false
	return t
}()

const jsonHex = "0123456789abcdef"

// JSONAppendString appends s quoted: '"', '\\' and the control bytes
// escaped (\n, \r, \t by name, the others as \u00XX), U+2028 and U+2029
// as \u2028 and \u2029 (as encoding/json, so JavaScript reads them), and
// each byte of invalid UTF-8 as the lone surrogate \udcXX of the byte
// (Python's surrogateescape, PEP 383), which Cursor reads back as the
// byte: an Ergo string holds bytes (types.md, Strings 1), so every string
// round-trips, and a valid UTF-8 string is plain JSON text. No HTML
// escaping (encoding/json v2's default).
func JSONAppendString(b []byte, s string) []byte { return jsonAppendString(b, s, &jsonSafe) }

// jsonAppendString is JSONAppendString with the ASCII bytes safe holds
// written as themselves.
func jsonAppendString(b []byte, s string, safe *[utf8.RuneSelf]bool) []byte {
	b = append(b, '"')
	start := 0
	for i := 0; i < len(s); {
		if c := s[i]; c < utf8.RuneSelf {
			if safe[c] {
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
				b = append(b, '\\', 'u', '0', '0', jsonHex[c>>4], jsonHex[c&0xf])
			}
			i++
			start = i
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			b = append(b, s[start:i]...)
			c := s[i]
			b = append(b, '\\', 'u', 'd', 'c', jsonHex[c>>4], jsonHex[c&0xf])
			i += size
			start = i
			continue
		}
		if r == '\u2028' || r == '\u2029' {
			b = append(b, s[start:i]...)
			b = append(b, '\\', 'u', '2', '0', '2', jsonHex[r&0xf])
			i += size
			start = i
			continue
		}
		i += size
	}
	b = append(b, s[start:]...)
	return append(b, '"')
}

// JSONDefaultDepth is the nesting limit of a MaxDepth that is not
// positive: encoding/json v2's.
const JSONDefaultDepth = 10000

// JSONOpen validates text whole, as one JSON value with white space around
// it, no longer than maxBytes bytes (no limit when maxBytes <= 0) and
// nested no deeper than maxDepth lists and objects (JSONDefaultDepth when
// maxDepth <= 0), and gives a Cursor at its start. It is phase 1 of
// decode: no value is built from a text that fails here.
func JSONOpen(text string, maxBytes int, maxDepth int) (*Cursor, error) {
	if maxBytes > 0 && len(text) > maxBytes {
		return nil, JSONMalformed{Offset: maxBytes, Reason: "the text is longer than MaxBytes " + strconv.Itoa(maxBytes)}
	}
	if maxDepth <= 0 {
		maxDepth = JSONDefaultDepth
	}
	v := jsonValidator{s: text, max: maxDepth}
	v.ws()
	if !v.value(0) {
		return nil, JSONMalformed{Offset: v.i, Reason: v.why}
	}
	v.ws()
	if v.i < len(text) {
		return nil, JSONMalformed{Offset: v.i, Reason: "text after the value"}
	}
	return &Cursor{s: text}, nil
}

func (e JSONMalformed) Error() string {
	return "json: " + e.Reason + " at offset " + strconv.Itoa(e.Offset)
}

// jsonValidator is a recursive-descent check of RFC 8259 text.
type jsonValidator struct {
	s   string
	i   int
	max int
	why string
}

func (v *jsonValidator) fail(why string) bool {
	if v.why == "" {
		v.why = why
	}
	return false
}

func (v *jsonValidator) ws() {
	for v.i < len(v.s) {
		switch v.s[v.i] {
		case ' ', '\t', '\n', '\r':
			v.i++
		default:
			return
		}
	}
}

func (v *jsonValidator) lit(word string) bool {
	if len(v.s)-v.i < len(word) || v.s[v.i:v.i+len(word)] != word {
		return v.fail("an invalid literal")
	}
	v.i += len(word)
	return true
}

func (v *jsonValidator) value(depth int) bool {
	if v.i >= len(v.s) {
		return v.fail("the text ends before a value")
	}
	switch c := v.s[v.i]; {
	case c == '{' || c == '[':
		if depth >= v.max {
			return v.fail("nesting deeper than MaxDepth " + strconv.Itoa(v.max))
		}
		return v.container(depth)
	case c == '"':
		return v.str()
	case c == 't':
		return v.lit("true")
	case c == 'f':
		return v.lit("false")
	case c == 'n':
		return v.lit("null")
	case c == '-' || (c >= '0' && c <= '9'):
		return v.num()
	}
	return v.fail("an unexpected byte")
}

func (v *jsonValidator) container(depth int) bool {
	open := v.s[v.i]
	closer := byte(']')
	if open == '{' {
		closer = '}'
	}
	v.i++
	v.ws()
	if v.i < len(v.s) && v.s[v.i] == closer {
		v.i++
		return true
	}
	for {
		if open == '{' {
			if v.i >= len(v.s) || v.s[v.i] != '"' {
				return v.fail("an object key that is not a string")
			}
			if !v.str() {
				return false
			}
			v.ws()
			if v.i >= len(v.s) || v.s[v.i] != ':' {
				return v.fail("no colon after an object key")
			}
			v.i++
			v.ws()
		}
		if !v.value(depth + 1) {
			return false
		}
		v.ws()
		if v.i >= len(v.s) {
			return v.fail("the text ends inside a list or object")
		}
		switch v.s[v.i] {
		case ',':
			v.i++
			v.ws()
		case closer:
			v.i++
			return true
		default:
			return v.fail("no comma or closing bracket")
		}
	}
}

func (v *jsonValidator) str() bool {
	v.i++
	for v.i < len(v.s) {
		c := v.s[v.i]
		switch {
		case c == '"':
			v.i++
			return true
		case c == '\\':
			v.i++
			if v.i >= len(v.s) {
				return v.fail("the text ends inside a string")
			}
			switch v.s[v.i] {
			case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
				v.i++
			case 'u':
				if len(v.s)-v.i < 5 {
					return v.fail("a short \\u escape")
				}
				for k := 1; k <= 4; k++ {
					if jsonUnhex(v.s[v.i+k]) < 0 {
						v.i += k
						return v.fail("a \\u escape that is not hexadecimal")
					}
				}
				v.i += 5
			default:
				return v.fail("an invalid escape")
			}
		case c < 0x20:
			return v.fail("a control byte inside a string")
		default:
			v.i++
		}
	}
	return v.fail("the text ends inside a string")
}

func (v *jsonValidator) digits() int {
	n := 0
	for v.i < len(v.s) && v.s[v.i] >= '0' && v.s[v.i] <= '9' {
		v.i++
		n++
	}
	return n
}

func (v *jsonValidator) num() bool {
	if v.s[v.i] == '-' {
		v.i++
	}
	if v.i < len(v.s) && v.s[v.i] == '0' {
		v.i++
	} else if v.digits() == 0 {
		return v.fail("a number with no digits")
	}
	if v.i < len(v.s) && v.s[v.i] == '.' {
		v.i++
		if v.digits() == 0 {
			return v.fail("no digits after a decimal point")
		}
	}
	if v.i < len(v.s) && (v.s[v.i] == 'e' || v.s[v.i] == 'E') {
		v.i++
		if v.i < len(v.s) && (v.s[v.i] == '+' || v.s[v.i] == '-') {
			v.i++
		}
		if v.digits() == 0 {
			return v.fail("no digits in an exponent")
		}
	}
	return true
}

func jsonUnhex(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	}
	return -1
}

// Cursor reads a text JSONOpen validated, for std json's Reader: each read
// takes the next value of the innermost open list or object, or the root.
// A read of the wrong kind gives false and leaves the value, which the
// caller's decode then abandons. Path is the place of the value last read
// or begun, built only when asked (a failure's path).
type Cursor struct {
	s      string
	i      int
	frames []jsonFrame
	root   bool // the root value has been read or begun
	wrong  bool // the last read met a value of another kind
}

// jsonFrame is an open list or object: for a list, how many elements were
// read or begun; for an object, the key of the member More last reached.
// Has says that the frame has a current element.
type jsonFrame struct {
	list bool
	n    int
	key  string
	has  bool
}

func (c *Cursor) ws() {
	for c.i < len(c.s) {
		switch c.s[c.i] {
		case ' ', '\t', '\n', '\r':
			c.i++
		default:
			return
		}
	}
}

// at starts a value: its place becomes the frame's current element.
func (c *Cursor) at() {
	c.ws()
	if n := len(c.frames); n > 0 {
		f := &c.frames[n-1]
		if f.list {
			f.n++
		}
		f.has = true
	} else {
		c.root = true
	}
}

// Path is the place of the value last read or begun: member names joined
// by dots, list indexes in brackets ("hosts[2].port"), "" for the root.
func (c *Cursor) Path() string {
	var b []byte
	for _, f := range c.frames {
		if !f.has {
			break
		}
		if f.list {
			b = append(b, '[')
			b = strconv.AppendInt(b, int64(f.n-1), 10)
			b = append(b, ']')
			continue
		}
		if len(b) > 0 {
			b = append(b, '.')
		}
		b = append(b, f.key...)
	}
	return string(b)
}

func (c *Cursor) word(w string) bool {
	if len(c.s)-c.i >= len(w) && c.s[c.i:c.i+len(w)] == w {
		c.i += len(w)
		return true
	}
	return false
}

func (c *Cursor) readBool() (bool, bool) {
	c.at()
	if c.word("true") {
		return true, true
	}
	if c.word("false") {
		return false, true
	}
	return false, false
}

// number is the text of the number at the cursor, taken, or false.
func (c *Cursor) number() (string, bool) {
	start := c.i
	for c.i < len(c.s) {
		switch ch := c.s[c.i]; {
		case ch >= '0' && ch <= '9', ch == '-', ch == '+', ch == '.', ch == 'e', ch == 'E':
			c.i++
			continue
		}
		break
	}
	return c.s[start:c.i], c.i > start
}

func (c *Cursor) readInt() (int64, bool) {
	c.at()
	text, ok := c.number()
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseInt(text, 10, 64)
	return n, err == nil
}

func (c *Cursor) readUint() (uint64, bool) {
	c.at()
	text, ok := c.number()
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseUint(text, 10, 64)
	return n, err == nil
}

// Float reads a number, or the strings "NaN", "Infinity" and "-Infinity"
// that JSONAppendFloat writes.
func (c *Cursor) readFloat() (float64, bool) {
	c.at()
	if c.i < len(c.s) && c.s[c.i] == '"' {
		switch {
		case c.word(`"NaN"`):
			return math.NaN(), true
		case c.word(`"Infinity"`):
			return math.Inf(1), true
		case c.word(`"-Infinity"`):
			return math.Inf(-1), true
		}
		return 0, false
	}
	text, ok := c.number()
	if !ok {
		return 0, false
	}
	f, err := strconv.ParseFloat(text, 64)
	return f, err == nil
}

func (c *Cursor) readString() (string, bool) {
	c.at()
	if c.i >= len(c.s) || c.s[c.i] != '"' {
		return "", false
	}
	return c.str(), true
}

// str takes the string at the cursor, its escapes read: the bytes of the
// text as they are, valid UTF-8 or not (a string holds bytes), \udc80 to
// \udcff as the byte JSONAppendString escaped, and any other lone
// surrogate as U+FFFD (encoding/json's rule).
func (c *Cursor) str() string {
	c.i++
	start := c.i
	for c.i < len(c.s) {
		ch := c.s[c.i]
		if ch == '"' {
			s := strings.Clone(c.s[start:c.i]) // a decoded string does not pin the text
			c.i++
			return s
		}
		if ch == '\\' {
			break
		}
		c.i++
	}
	b := []byte(c.s[start:c.i])
	for c.i < len(c.s) {
		ch := c.s[c.i]
		switch {
		case ch == '"':
			c.i++
			return string(b)
		case ch == '\\':
			c.i++
			e := c.s[c.i]
			c.i++
			switch e {
			case 'b':
				b = append(b, '\b')
			case 'f':
				b = append(b, '\f')
			case 'n':
				b = append(b, '\n')
			case 'r':
				b = append(b, '\r')
			case 't':
				b = append(b, '\t')
			case 'u':
				r := c.hex4()
				if r >= 0xD800 && r < 0xDC00 && len(c.s)-c.i >= 6 && c.s[c.i] == '\\' && c.s[c.i+1] == 'u' {
					save := c.i
					c.i += 2
					if r2 := c.hex4(); r2 >= 0xDC00 && r2 < 0xE000 {
						r = (r-0xD800)<<10 + (r2 - 0xDC00) + 0x10000
					} else {
						c.i = save
					}
				}
				switch {
				case r >= 0xDC80 && r <= 0xDCFF:
					b = append(b, byte(r-0xDC00)) // surrogateescape: the byte JSONAppendString escaped
				case r >= 0xD800 && r < 0xE000:
					b = utf8.AppendRune(b, utf8.RuneError)
				default:
					b = utf8.AppendRune(b, r)
				}
			default: // '"', '\\', '/'
				b = append(b, e)
			}
		default:
			b = append(b, ch)
			c.i++
		}
	}
	return string(b)
}

func (c *Cursor) hex4() rune {
	var r rune
	for k := 0; k < 4; k++ {
		r = r<<4 | rune(jsonUnhex(c.s[c.i+k]))
	}
	c.i += 4
	return r
}

func (c *Cursor) begin(open byte, list bool) bool {
	c.at()
	if c.i >= len(c.s) || c.s[c.i] != open {
		return false
	}
	c.i++
	c.frames = append(c.frames, jsonFrame{list: list})
	return true
}

func (c *Cursor) BeginList() bool { return c.begin('[', true) }

func (c *Cursor) BeginObject() bool { return c.begin('{', false) }

// Tag is the string value of the member "_tag" of the object just begun,
// wherever it stands among the members, or false. The member stays: a
// record's decode skips it as a key it does not know.
func (c *Cursor) readTag() (string, bool) {
	save := c.i
	defer func() { c.i = save }()
	for {
		c.ws()
		if c.i < len(c.s) && c.s[c.i] == ',' {
			c.i++
			c.ws()
		}
		if c.i >= len(c.s) || c.s[c.i] != '"' {
			return "", false
		}
		k := c.str()
		c.ws()
		c.i++ // ':'
		c.ws()
		if k == "_tag" && c.i < len(c.s) && c.s[c.i] == '"' {
			return c.str(), true
		}
		c.skip()
	}
}

// More reports whether the innermost list or object has a value left; in
// an object it takes the member's key, which Key gives.
func (c *Cursor) More() bool {
	c.ws()
	if c.i < len(c.s) && c.s[c.i] == ',' {
		c.i++
		c.ws()
	}
	if c.i >= len(c.s) || c.s[c.i] == ']' || c.s[c.i] == '}' {
		return false
	}
	if n := len(c.frames); n > 0 && !c.frames[n-1].list {
		f := &c.frames[n-1]
		f.key = c.str()
		f.has = true
		c.ws()
		c.i++ // ':'
	}
	return true
}

// Key is the key of the member More reached.
func (c *Cursor) Key() string {
	if n := len(c.frames); n > 0 {
		return c.frames[n-1].key
	}
	return ""
}

// Skip passes the next value whole.
func (c *Cursor) Skip() {
	c.at()
	c.skip()
}

func (c *Cursor) skip() {
	c.ws()
	if c.i >= len(c.s) {
		return
	}
	switch c.s[c.i] {
	case '"':
		c.i++
		for c.i < len(c.s) {
			switch c.s[c.i] {
			case '\\':
				c.i += 2
				continue
			case '"':
				c.i++
				return
			}
			c.i++
		}
	case '{', '[':
		depth := 0
		for c.i < len(c.s) {
			switch c.s[c.i] {
			case '"':
				c.skip()
				continue
			case '{', '[':
				depth++
			case '}', ']':
				depth--
				if depth == 0 {
					c.i++
					return
				}
			}
			c.i++
		}
	default:
		for c.i < len(c.s) {
			switch c.s[c.i] {
			case ',', ']', '}', ' ', '\t', '\n', '\r':
				return
			}
			c.i++
		}
	}
}

// End closes the innermost list or object, passing what is left of it;
// Path is its place again.
func (c *Cursor) End() {
	for c.More() {
		c.skip()
	}
	if c.i < len(c.s) {
		c.i++
	}
	if n := len(c.frames); n > 0 {
		c.frames = c.frames[:n-1]
	}
}

// The reads of std json's Cursor give the value and set Wrong, which says
// that the read met a value of another kind (or, for Tag, an object with
// no "_tag"), so the Reader's Ergo code builds its failure with no Option
// box per read.

// Wrong reports that the last value read was not of the kind asked for.
func (c *Cursor) Wrong() bool { return c.wrong }

func (c *Cursor) Bool() bool {
	v, ok := c.readBool()
	c.wrong = !ok
	return v
}

func (c *Cursor) Int() int64 {
	v, ok := c.readInt()
	c.wrong = !ok
	return v
}

func (c *Cursor) Uint() uint64 {
	v, ok := c.readUint()
	c.wrong = !ok
	return v
}

func (c *Cursor) Float() float64 {
	v, ok := c.readFloat()
	c.wrong = !ok
	return v
}

func (c *Cursor) String() string {
	v, ok := c.readString()
	c.wrong = !ok
	return v
}

func (c *Cursor) Tag() string {
	v, ok := c.readTag()
	c.wrong = !ok
	return v
}

// jsonPool keeps the buffers of the Texts Codec.Encode writes (JSONTake,
// JSONGive), as encoding/json keeps its encodeState: a text's buffer
// grows once and is used again.
var jsonPool = sync.Pool{New: func() any { return &Text{B: make([]byte, 0, 1024)} }}

// JSONTake is an empty Text from the pool, for std json's Encode only:
// JSONGive takes it back.
func JSONTake() *Text { return jsonPool.Get().(*Text) }

// JSONGive is the text t wrote, and puts t back in the pool; nothing may
// use t after. A buffer that grew past 64 KiB is dropped, so the pool
// holds no large buffer.
func JSONGive(t *Text) string {
	s := string(t.B)
	if cap(t.B) <= 64<<10 {
		t.B, t.Comma = t.B[:0], false
		jsonPool.Put(t)
	}
	return s
}
