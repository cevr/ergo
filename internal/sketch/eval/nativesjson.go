package eval

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// std json's Go functions (ORACLE O18), from the package's doc comments
// (std/json/json.ergo) and Go's stdlib, sharing no code with runtime/v1:
//
//   - std json's Text is Ergo (LR3): the evaluator runs its body, and its
//     appenders are natives here: an integer's exact digits; a float as
//     encoding/json writes it, NaN and the infinities as the strings
//     "NaN", "Infinity" and "-Infinity"; a string with '"', '\' and the
//     control bytes escaped (\n, \r and \t by name, the others \u00XX),
//     U+2028 and U+2029 as escapes, each byte of invalid UTF-8 as \udcXX,
//     and when html <, > and & as \u003c, \u003e and \u0026; the text is
//     bytesText (std strings' Borrow, Give and Hint keep no pool here);
//   - open checks the whole text (phase 1) and fails Malformed at the
//     first byte that breaks a rule; it parses the text into a tree once,
//     and a Cursor walks the tree.
func registerNativesjson(t nativeTables) {
	natives := t.std
	t.reads["json.appendQuoted"] = "sliceV string bool"
	natives["json.appendQuoted"] = jsonAppend(func(args []Value) string { return jsonQuote(args[0].(string), args[1].(bool)) })
	t.reads["json.appendInt"] = "sliceV int64"
	natives["json.appendInt"] = jsonAppend(func(args []Value) string { return strconv.FormatInt(args[0].(int64), 10) })
	t.reads["json.appendUint"] = "sliceV uint64"
	natives["json.appendUint"] = jsonAppend(func(args []Value) string { return strconv.FormatUint(args[0].(uint64), 10) })
	t.reads["json.appendBool"] = "sliceV bool"
	natives["json.appendBool"] = jsonAppend(func(args []Value) string { return strconv.FormatBool(args[0].(bool)) })
	t.reads["json.appendFloat"] = "sliceV float64"
	natives["json.appendFloat"] = jsonAppend(func(args []Value) string { return jsonFloat(args[0].(float64)) })
	t.reads["json.bytesText"] = "sliceV"
	natives["json.bytesText"] = bytesText
	// std strings' borrowed buffer (NOPRIV-JSON): the evaluator keeps no
	// pool, so Hint is no hint, Borrow an empty buffer and Give its buffer
	// unchanged; builderText is FromBytes' copy.
	t.reads["strings.Hint"] = ""
	natives["strings.Hint"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		return int64(0), in.charge(fr, at, 1)
	}
	t.reads["strings.Borrow"] = "int64"
	natives["strings.Borrow"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		return newSlice(nil), in.charge(fr, at, 1)
	}
	t.reads["strings.Give"] = "sliceV int64"
	natives["strings.Give"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		return args[0], in.charge(fr, at, 1)
	}
	t.reads["strings.builderText"] = "sliceV"
	natives["strings.builderText"] = bytesText
	// std build's InFold, Known and Hole (CF1, CF2): the evaluator is what
	// computes a build-time fold, so in a fold InFold is true, where
	// runtime/v1.BuildInFold, the built program's, is false; a value is
	// known unless it holds a template's hole (a symbol). Outside a fold (a
	// law at check time) they answer as a built program does. They are no
	// parity rows: the two differ by design, and a function that branches
	// on them states a law that its branches agree (std/build).
	t.reads["build.InFold"] = ""
	natives["build.InFold"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		return in.fold, in.charge(fr, at, 1)
	}
	t.reads["build.Known"] = "T"
	holeNative(t, "build.Known", func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		return in.fold && !symbolic(args[0]), in.charge(fr, at, 1)
	})
	t.reads["build.Hole"] = "T"
	holeNative(t, "build.Hole", func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		_, hole := args[0].(*symbol)
		return in.fold && hole, in.charge(fr, at, 1)
	})
	// std strings' Builder writes (SSR WEB3: std hmac builds its blocks
	// with them): WriteString and WriteByte append as Go's append grows
	// the buffer; Grow keeps no room here, so it is the buffer unchanged,
	// and a negative count is Go's panic.
	t.reads["strings.builderAppend"] = "sliceV string"
	t.reads["strings.builderAppendByte"] = "sliceV uint8"
	t.reads["strings.builderGrow"] = "sliceV int64"
	natives["strings.builderAppend"] = jsonAppend(func(args []Value) string { return args[0].(string) })
	natives["strings.builderAppendByte"] = jsonAppend(func(args []Value) string { return string([]byte{args[0].(uint8)}) })
	natives["strings.builderGrow"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		if args[1].(int64) < 0 {
			return nil, fr.defect(at, "strings.Builder.Grow: negative count")
		}
		return args[0], in.charge(fr, at, 1)
	}
	// MX-EVBUILDER: the rest of the Builder's writes, and std civil's
	// buffer, on the same appends.
	t.reads["strings.builderAppendBytes"] = "sliceV sliceV"
	natives["strings.builderAppendBytes"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		out, ok := args[0].(sliceV)
		if !ok {
			return nil, fr.unsupported(at, "%s on bytes the evaluator does not hold", fn.Name)
		}
		var v Value = out
		for _, c := range args[1].(sliceV).elems() {
			var ab *abrupt
			if v, ab = in.appendTo(fr, at, v.(sliceV), c, nil); ab != nil {
				return nil, ab
			}
		}
		return v, nil
	}
	t.reads["strings.builderAppendFrom"] = "sliceV int64 int64"
	natives["strings.builderAppendFrom"] = builderAppendFrom
	t.reads["civil.buffer"] = "int64"
	natives["civil.buffer"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		// Go's make([]byte, 0, n) for civil's constant n: the room is
		// the Go target's, so an empty buffer, as strings.Borrow's.
		return newSlice(nil), in.charge(fr, at, 1)
	}
	t.reads["civil.bytesText"] = "sliceV"
	natives["civil.bytesText"] = bytesText
	// openChecked and settleText are Decode's one pass (JSON-VALUE-DECODE).
	// The evaluator checks the text whole as open does, so a text that is
	// not JSON fails before the decode, which is the outcome settleText gives,
	// and settleText has nothing left to find.
	t.reads["json.isNumber"] = "string"
	natives["json.isNumber"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		// One number with nothing around it: the parser's number, the whole text.
		text := args[0].(string)
		n, _, why := jsonParse(text, 0, 0)
		number := why == "" && n.kind == 2 && strings.TrimSpace(text) == text
		return number, in.charge(fr, at, 1+argWork(args))
	}
	t.reads["json.openChecked"] = "string int64 int64"
	natives["json.openChecked"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		return natives["json.open"](in, fr, at, fn, args)
	}
	t.reads["json.settleText"] = "host:json.Cursor"
	natives["json.settleText"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		return unit{}, in.charge(fr, at, 1)
	}
	t.reads["json.open"] = "string int64 int64"
	natives["json.open"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		if ab := in.charge(fr, at, 1+argWork(args)); ab != nil {
			return nil, ab
		}
		text, maxBytes, maxDepth := args[0].(string), args[1].(int64), args[2].(int64)
		root, off, why := jsonParse(text, maxBytes, maxDepth)
		if why != "" {
			return nil, fr.abrupt(ctlFail, at, stdRecord(fn, "Malformed", map[string]Value{"Offset": int64(off), "Reason": why}), "")
		}
		if ab := in.allocate(fr, at, int64(len(text))*2); ab != nil {
			return nil, ab
		}
		return &jsonCursor{root: root}, nil
	}
}

// bytesText is the text of a buffer of bytes the evaluator holds.
func bytesText(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
	sl, ok := args[0].(sliceV)
	out, known := "", ok
	if ok {
		out, known = byteString(sl)
	}
	if !known {
		return nil, fr.unsupported(at, "%s of bytes the evaluator does not hold", fn.Name)
	}
	if ab := in.charge(fr, at, 1+int64(len(out))); ab != nil {
		return nil, ab
	}
	return out, in.allocate(fr, at, int64(len(out)))
}

// jsonAppend is the native of an appender of std json's Text: its buffer,
// args[0], with the text of the other arguments at its end, a byte at a
// time as Go's append grows it.
func jsonAppend(text func(args []Value) string) native {
	return func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		out, ok := args[0].(sliceV)
		if !ok {
			return nil, fr.unsupported(at, "%s on bytes the evaluator does not hold", fn.Name)
		}
		var v Value = out
		for _, c := range []byte(text(args[1:])) {
			var ab *abrupt
			if v, ab = in.appendTo(fr, at, v.(sliceV), c, nil); ab != nil {
				return nil, ab
			}
		}
		return v, nil
	}
}

// jsonFloat is f as encoding/json writes it; NaN and the infinities as
// strings.
func jsonFloat(f float64) string {
	switch {
	case math.IsNaN(f):
		return `"NaN"`
	case math.IsInf(f, 1):
		return `"Infinity"`
	case math.IsInf(f, -1):
		return `"-Infinity"`
	}
	b, err := json.Marshal(f)
	if err != nil {
		ice("encoding/json refused %v", f)
	}
	return string(b)
}

// jsonQuote is s as a JSON string (see init).
func jsonQuote(s string, html bool) string {
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && size == 1:
			b.WriteString(`\udc` + hex2(s[i]))
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r < 0x20:
			b.WriteString(`\u00` + hex2(byte(r)))
		case r == '\u2028' || r == '\u2029':
			b.WriteString(`\u202` + strconv.FormatInt(int64(r&0xf), 16))
		case html && (r == '<' || r == '>' || r == '&'):
			b.WriteString(`\u00` + hex2(byte(r)))
		default:
			b.WriteString(s[i : i+size])
		}
		i += size
	}
	b.WriteByte('"')
	return b.String()
}

func hex2(c byte) string {
	const digits = "0123456789abcdef"
	return string([]byte{digits[c>>4], digits[c&0xf]})
}

// jsonNode is one value of a validated text. Kind is WireKind's order:
// 0 null, 1 bool, 2 number, 3 string, 4 list, 5 object. Text is a
// number's spelling, a string's decoded bytes or a literal's word; an
// object's members are keys[i] and elems[i].
type jsonNode struct {
	kind  int
	text  string
	raw   string // a string's spelling, quotes and escapes as written
	span  string // the value's text as written (json.Cursor SkipText)
	keys  []string
	elems []*jsonNode
}

// jsonParser checks and parses RFC 8259 text: the first byte that breaks
// a rule stops it, with the rule.
type jsonParser struct {
	s     string
	i     int
	depth int64
	why   string
}

func (p *jsonParser) fail(why string) *jsonNode {
	p.why = why
	return nil
}

func (p *jsonParser) space() {
	for p.i < len(p.s) && strings.IndexByte(" \t\n\r", p.s[p.i]) >= 0 {
		p.i++
	}
}

// jsonParse is the tree of text, or the offset and rule of its first
// break: longer than maxBytes (when positive), nested deeper than
// maxDepth (10000 when not positive), or not one JSON value with white
// space around it.
func jsonParse(text string, maxBytes, maxDepth int64) (*jsonNode, int, string) {
	if maxBytes > 0 && int64(len(text)) > maxBytes {
		return nil, int(maxBytes), "the text is longer than MaxBytes " + strconv.FormatInt(maxBytes, 10)
	}
	if maxDepth <= 0 {
		maxDepth = 10000
	}
	p := &jsonParser{s: text, depth: maxDepth}
	p.space()
	n := p.value(0)
	if n == nil {
		return nil, p.i, p.why
	}
	p.space()
	if p.i < len(p.s) {
		return nil, p.i, "text after the value"
	}
	return n, 0, ""
}

// value parses one value and keeps its text as its span.
func (p *jsonParser) value(depth int64) *jsonNode {
	start := p.i
	n := p.valueAt(depth)
	if n != nil {
		n.span = p.s[start:p.i]
	}
	return n
}

func (p *jsonParser) valueAt(depth int64) *jsonNode {
	if p.i >= len(p.s) {
		return p.fail("the text ends before a value")
	}
	c := p.s[p.i]
	switch {
	case c == '[' || c == '{':
		if depth >= p.depth {
			return p.fail("nesting deeper than MaxDepth " + strconv.FormatInt(p.depth, 10))
		}
		return p.container(depth)
	case c == '"':
		start := p.i
		s, ok := p.str()
		if !ok {
			return nil
		}
		return &jsonNode{kind: 3, text: s, raw: p.s[start:p.i]}
	case c == 't' || c == 'f' || c == 'n':
		for _, w := range []string{"true", "false", "null"} {
			if strings.HasPrefix(p.s[p.i:], w) {
				p.i += len(w)
				kind := 1
				if w == "null" {
					kind = 0
				}
				return &jsonNode{kind: kind, text: w}
			}
		}
		return p.fail("an invalid literal")
	case c == '-' || (c >= '0' && c <= '9'):
		return p.number()
	}
	_, size := utf8.DecodeRuneInString(p.s[p.i:])
	return p.fail("an unexpected " + strconv.Quote(p.s[p.i:p.i+size]) + " where a value belongs")
}

func (p *jsonParser) container(depth int64) *jsonNode {
	object := p.s[p.i] == '{'
	closer := byte(']')
	n := &jsonNode{kind: 4}
	if object {
		closer, n.kind = '}', 5
	}
	p.i++
	p.space()
	if p.i < len(p.s) && p.s[p.i] == closer {
		p.i++
		return n
	}
	for {
		if object {
			if p.i >= len(p.s) || p.s[p.i] != '"' {
				return p.fail("an object key that is not a string")
			}
			k, ok := p.str()
			if !ok {
				return nil
			}
			p.space()
			if p.i >= len(p.s) || p.s[p.i] != ':' {
				return p.fail("no colon after an object key")
			}
			p.i++
			p.space()
			n.keys = append(n.keys, k)
		}
		e := p.value(depth + 1)
		if e == nil {
			return nil
		}
		n.elems = append(n.elems, e)
		p.space()
		if p.i >= len(p.s) {
			return p.fail("the text ends inside a list or object")
		}
		switch p.s[p.i] {
		case ',':
			p.i++
			p.space()
		case closer:
			p.i++
			return n
		default:
			return p.fail("no comma or closing bracket")
		}
	}
}

func (p *jsonParser) digits() int {
	start := p.i
	for p.i < len(p.s) && p.s[p.i] >= '0' && p.s[p.i] <= '9' {
		p.i++
	}
	return p.i - start
}

func (p *jsonParser) number() *jsonNode {
	start := p.i
	if p.s[p.i] == '-' {
		p.i++
	}
	if p.i < len(p.s) && p.s[p.i] == '0' {
		p.i++
	} else if p.digits() == 0 {
		return p.fail("a number with no digits")
	}
	if p.i < len(p.s) && p.s[p.i] == '.' {
		p.i++
		if p.digits() == 0 {
			return p.fail("no digits after a decimal point")
		}
	}
	if p.i < len(p.s) && (p.s[p.i] == 'e' || p.s[p.i] == 'E') {
		p.i++
		if p.i < len(p.s) && (p.s[p.i] == '+' || p.s[p.i] == '-') {
			p.i++
		}
		if p.digits() == 0 {
			return p.fail("no digits in an exponent")
		}
	}
	return &jsonNode{kind: 2, text: p.s[start:p.i]}
}

func unhex(c byte) (rune, bool) {
	switch {
	case c >= '0' && c <= '9':
		return rune(c - '0'), true
	case c >= 'a' && c <= 'f':
		return rune(c-'a') + 10, true
	case c >= 'A' && c <= 'F':
		return rune(c-'A') + 10, true
	}
	return 0, false
}

// hex4 is the code of the \u escape whose four digits start at i.
func (p *jsonParser) hex4(i int) (rune, bool) {
	if len(p.s)-i < 4 {
		return 0, false
	}
	var r rune
	for k := 0; k < 4; k++ {
		d, ok := unhex(p.s[i+k])
		if !ok {
			return 0, false
		}
		r = r<<4 | d
	}
	return r, true
}

// str checks and decodes the string at p.i: its bytes as they are,
// \udc80 to \udcff as the byte, a surrogate pair as its character, any
// other lone surrogate as U+FFFD.
func (p *jsonParser) str() (string, bool) {
	p.i++
	var b []byte
	for p.i < len(p.s) {
		c := p.s[p.i]
		switch {
		case c == '"':
			p.i++
			return string(b), true
		case c < 0x20:
			p.fail("a control byte inside a string")
			return "", false
		case c != '\\':
			b = append(b, c)
			p.i++
			continue
		}
		p.i++
		if p.i >= len(p.s) {
			p.fail("the text ends inside a string")
			return "", false
		}
		e := p.s[p.i]
		if named := strings.IndexByte(`"\/bfnrt`, e); named >= 0 {
			b = append(b, "\"\\/\b\f\n\r\t"[named])
			p.i++
			continue
		}
		if e != 'u' {
			p.fail("an invalid escape")
			return "", false
		}
		if len(p.s)-p.i < 5 {
			p.fail("a short \\u escape")
			return "", false
		}
		for k := 1; k <= 4; k++ {
			if _, ok := unhex(p.s[p.i+k]); !ok {
				p.i += k
				p.fail("a \\u escape that is not hexadecimal")
				return "", false
			}
		}
		r, _ := p.hex4(p.i + 1)
		p.i += 5
		if r >= 0xD800 && r < 0xDC00 && strings.HasPrefix(p.s[p.i:], `\u`) {
			if low, ok := p.hex4(p.i + 2); ok && low >= 0xDC00 && low < 0xE000 {
				r = (r-0xD800)<<10 + (low - 0xDC00) + 0x10000
				p.i += 6
			}
		}
		switch {
		case r >= 0xDC80 && r <= 0xDCFF:
			b = append(b, byte(r-0xDC00))
		case r >= 0xD800 && r < 0xE000:
			b = utf8.AppendRune(b, utf8.RuneError)
		default:
			b = utf8.AppendRune(b, r)
		}
	}
	p.fail("the text ends inside a string")
	return "", false
}

// jsonCursor is a json.Cursor over a parsed text. Each open list or
// object is a frame: its node, the index of its next element, and its
// current element (the place Path names): a list's count of elements
// read or begun, an object's key More last reached.
type jsonCursor struct {
	root   *jsonNode
	rooted bool // the root value was read or begun
	done   bool // the root value was taken
	frames []*jsonFrameV
	wrong  bool
}

type jsonFrameV struct {
	node *jsonNode
	next int
	n    int    // a list's elements read or begun
	key  string // an object's member More reached
	has  bool
}

func (c *jsonCursor) describe() string { return "a json.Cursor" }

// peek is the next value, nil at the end of the innermost list or object.
func (c *jsonCursor) peek() *jsonNode {
	if len(c.frames) == 0 {
		if c.done {
			return nil
		}
		return c.root
	}
	f := c.frames[len(c.frames)-1]
	if f.next >= len(f.node.elems) {
		return nil
	}
	return f.node.elems[f.next]
}

// at makes the next value's place the current one.
func (c *jsonCursor) at() {
	if len(c.frames) == 0 {
		c.rooted = true
		return
	}
	f := c.frames[len(c.frames)-1]
	if !f.node.object() {
		f.n++
	}
	f.has = true
}

// take passes the next value.
func (c *jsonCursor) take() {
	if len(c.frames) == 0 {
		c.done = true
		return
	}
	c.frames[len(c.frames)-1].next++
}

func (n *jsonNode) object() bool { return n.kind == 5 }

func (c *jsonCursor) path() string {
	var b strings.Builder
	for _, f := range c.frames {
		if !f.has {
			break
		}
		if !f.node.object() {
			b.WriteString("[" + strconv.Itoa(f.n-1) + "]")
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('.')
		}
		b.WriteString(f.key)
	}
	return b.String()
}

// read reads the next value as kind: a number is taken once it is one,
// even when its spelling does not parse as asked (ok false).
func (c *jsonCursor) read(kinds ...int) *jsonNode {
	c.at()
	n := c.peek()
	if n == nil {
		return nil
	}
	for _, k := range kinds {
		if n.kind == k {
			c.take()
			return n
		}
	}
	return nil
}

func (c *jsonCursor) opCall(in *interp, fr *frame, at syntax.Node, name string, args []Value) (Value, *abrupt) {
	if ab := in.charge(fr, at, 1); ab != nil {
		return nil, ab
	}
	var out Value
	switch name {
	case "TakeNull":
		if n := c.peek(); n != nil && n.kind == 0 {
			c.at()
			c.take()
			return true, nil
		}
		return false, nil
	case "Kind":
		if n := c.peek(); n != nil {
			return int64(n.kind), nil
		}
		return int64(6), nil
	case "Path":
		out = c.path()
	case "Wrong":
		return c.wrong, nil
	case "Bool":
		n := c.read(1)
		c.wrong = n == nil
		out = n != nil && n.text == "true"
	case "Number":
		n := c.read(2)
		c.wrong = n == nil
		out = ""
		if n != nil {
			out = n.text
		}
	case "Int":
		var v int64
		n := c.read(2)
		c.wrong = n == nil
		if n != nil {
			var err error
			v, err = strconv.ParseInt(n.text, 10, 64)
			c.wrong = err != nil
			if err != nil {
				v = 0
			}
		}
		out = v
	case "Uint":
		var v uint64
		n := c.read(2)
		c.wrong = n == nil
		if n != nil {
			var err error
			v, err = strconv.ParseUint(n.text, 10, 64)
			c.wrong = err != nil
			if err != nil {
				v = 0
			}
		}
		out = v
	case "Float":
		out, c.wrong = c.float()
	case "String":
		n := c.read(3)
		c.wrong = n == nil
		out = ""
		if n != nil {
			out = n.text
		}
	case "StringBytes":
		// into with the next string's bytes added, as String reads them
		// (GX12).
		n := c.read(3)
		c.wrong = n == nil
		s := ""
		if n != nil {
			s = n.text
		}
		if ab := in.allocate(fr, at, int64(len(s))); ab != nil {
			return nil, ab
		}
		return appendString(nil, args[0], s), nil
	case "BeginList", "BeginObject":
		kind := 4
		if name == "BeginObject" {
			kind = 5
		}
		// The container stays the parent's next value until End passes
		// it.
		c.at()
		n := c.peek()
		if n == nil || n.kind != kind {
			return false, nil
		}
		c.frames = append(c.frames, &jsonFrameV{node: n})
		return true, nil
	case "Tag":
		out, c.wrong = "", true
		if len(c.frames) > 0 {
			f := c.frames[len(c.frames)-1]
			for i := f.next; i < len(f.node.elems); i++ {
				if f.node.keys[i] == "_tag" && f.node.elems[i].kind == 3 {
					out, c.wrong = f.node.elems[i].text, false
					break
				}
			}
		}
	case "More":
		return c.more(), nil
	case "Key":
		out = ""
		if len(c.frames) > 0 {
			out = c.frames[len(c.frames)-1].key
		}
	case "Skip":
		c.at()
		if c.peek() != nil {
			c.take()
		}
		return unit{}, nil
	case "SkipText":
		// Skip, giving the text it passed: the value's span, "" at the end.
		// A substring of the input, as the built Cursor's: no allocation.
		c.at()
		if n := c.peek(); n != nil {
			c.take()
			return n.span, nil
		}
		return "", nil
	case "End":
		for c.more() {
			c.take()
		}
		if len(c.frames) > 0 {
			c.frames = c.frames[:len(c.frames)-1]
			if len(c.frames) == 0 {
				c.done = true
			} else {
				c.take()
			}
		}
		return unit{}, nil
	default:
		ice("the json.Cursor operation %s", name)
	}
	if s, ok := out.(string); ok {
		if ab := in.allocate(fr, at, int64(len(s))); ab != nil {
			return nil, ab
		}
	}
	return out, nil
}

// more reports a next value in the innermost list or object; in an object
// it reaches the member, whose key Key gives.
func (c *jsonCursor) more() bool {
	n := c.peek()
	if n == nil {
		return false
	}
	if len(c.frames) > 0 {
		if f := c.frames[len(c.frames)-1]; f.node.object() {
			f.key, f.has = f.node.keys[f.next], true
		}
	}
	return true
}

// float reads a number, or one of the strings "NaN", "Infinity" and
// "-Infinity"; wrong is true for any other value.
func (c *jsonCursor) float() (float64, bool) {
	c.at()
	n := c.peek()
	if n == nil {
		return 0, true
	}
	switch n.kind {
	case 3:
		// The spelling as written: an escaped "NaN" is a string.
		special := map[string]float64{`"NaN"`: math.NaN(), `"Infinity"`: math.Inf(1), `"-Infinity"`: math.Inf(-1)}
		f, ok := special[n.raw]
		if !ok {
			return 0, true
		}
		c.take()
		return f, false
	case 2:
		c.take()
		f, err := strconv.ParseFloat(n.text, 64)
		if err != nil {
			return 0, true
		}
		return f, false
	}
	return 0, true
}
