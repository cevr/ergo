package v1

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// TOMLMalformed is std toml's syntax/limit failure. Line counts from one;
// Key is the dotted path being read (empty before a key is known).
type TOMLMalformed struct {
	Line        int
	Key, Reason string
}

func (e TOMLMalformed) Error() string {
	return fmt.Sprintf("toml line %d key %q: %s", e.Line, e.Key, e.Reason)
}

// TOMLParse validates a TOML 1.0 document and writes its value in the JSON
// Reader's representation. The shared derived decoder then checks the
// nominal record, ParseX functions, refinements and typed failure rows.
// There is no host I/O, reflection, or external module dependency.
// Dates/times are strings; nonfinite floats use json's existing strings.
func TOMLParse(text string, maxBytes, maxDepth int) (out string, err error) {
	// Only our syntax failure is recovered. A defect remains a defect.
	defer func() {
		if e := recover(); e != nil {
			if m, ok := e.(TOMLMalformed); ok {
				err = m
			} else {
				panic(e)
			}
		}
	}()
	p := tomlParser{line: 1, maxDepth: maxDepth}
	if maxBytes > 0 && len(text) > maxBytes {
		p.fail("document exceeds MaxBytes")
	}
	if !utf8.ValidString(text) {
		for rest := text; rest != ""; {
			r, n := utf8.DecodeRuneInString(rest)
			if r == utf8.RuneError && n == 1 {
				break
			}
			if r == '\n' {
				p.line++
			}
			rest = rest[n:]
		}
		p.fail("document is not UTF-8")
	}
	p.text = strings.ReplaceAll(text, "\r\n", "\n")
	if p.maxDepth <= 0 {
		p.maxDepth = 10000
	}
	root := tomlObject(false)
	now := root
	var path []string
	for {
		p.space(true)
		if p.end() {
			break
		}
		p.key = strings.Join(path, ".")
		if p.peek() == '[' {
			p.key = ""
			p.next()
			array := p.peek() == '['
			if array {
				p.next()
			}
			path = p.keys()
			p.key = strings.Join(path, ".")
			p.expect(']')
			if array {
				p.expect(']')
			}
			now = p.table(root, path, array)
		} else {
			p.assignment(now, path, 0)
		}
		p.space(false)
		if !p.end() && p.peek() != '\n' {
			p.fail("expected end of line")
		}
	}
	return string(root.json(nil)), nil
}

// A node keeps table identity separate from value shape: a literal array
// of tables is not an array-of-tables header; a dotted/explicit/inline
// table cannot be redeclared by a header. JSON conversion erases these
// parser-only facts after the whole document is validated.
type tomlNode struct {
	kind                         byte // o object, a array, s string, n number, b bool
	text                         string
	fields                       map[string]*tomlNode
	items                        []*tomlNode
	defined, inline, aot, header bool
}

func tomlObject(defined bool) *tomlNode {
	return &tomlNode{kind: 'o', fields: map[string]*tomlNode{}, defined: defined}
}

func (n *tomlNode) json(b []byte) []byte {
	switch n.kind {
	case 's':
		return JSONAppendString(b, n.text)
	case 'n', 'b':
		return append(b, n.text...)
	case 'a':
		b = append(b, '[')
		for i, v := range n.items {
			if i > 0 {
				b = append(b, ',')
			}
			b = v.json(b)
		}
		return append(b, ']')
	default:
		b = append(b, '{')
		keys := make([]string, 0, len(n.fields))
		for k := range n.fields {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for i, k := range keys {
			if i > 0 {
				b = append(b, ',')
			}
			b = JSONAppendString(b, k)
			b = append(b, ':')
			b = n.fields[k].json(b)
		}
		return append(b, '}')
	}
}

type tomlParser struct {
	text               string
	at, line, maxDepth int
	key                string
}

func (p *tomlParser) fail(why string) { panic(TOMLMalformed{Line: p.line, Key: p.key, Reason: why}) }
func (p *tomlParser) end() bool       { return p.at == len(p.text) }
func (p *tomlParser) peek() byte {
	if p.end() {
		return 0
	}
	return p.text[p.at]
}
func (p *tomlParser) next() byte {
	if p.end() {
		p.fail("unexpected end of document")
	}
	b := p.text[p.at]
	p.at++
	if b == '\n' {
		p.line++
	}
	return b
}
func (p *tomlParser) expect(b byte) {
	if p.peek() != b || p.end() {
		p.fail("expected " + strconv.QuoteRune(rune(b)))
	}
	p.next()
}

// space skips comments only where the grammar permits them. An inline
// table does not call it across a line; array values do.
func (p *tomlParser) space(lines bool) {
	for !p.end() {
		switch p.peek() {
		case ' ', '\t':
			p.next()
		case '\n':
			if !lines {
				return
			}
			p.next()
		case '#':
			for !p.end() && p.peek() != '\n' {
				b := p.next()
				if b < 32 && b != '\t' || b == 127 {
					p.fail("control character in comment")
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

func tomlBare(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '-' || b == '_'
}

func (p *tomlParser) keys() []string {
	var keys []string
	for {
		for p.peek() == ' ' || p.peek() == '\t' {
			p.next()
		}
		if p.peek() == '"' || p.peek() == '\'' {
			keys = append(keys, p.quoted(false))
		} else {
			start := p.at
			for !p.end() && tomlBare(p.peek()) {
				p.next()
			}
			if p.at == start {
				p.fail("expected key")
			}
			keys = append(keys, p.text[start:p.at])
		}
		for p.peek() == ' ' || p.peek() == '\t' {
			p.next()
		}
		if p.peek() != '.' {
			return keys
		}
		p.next()
	}
}

func (p *tomlParser) descend(n *tomlNode, keys []string, dotted bool) *tomlNode {
	for _, k := range keys {
		if n.inline {
			p.fail("cannot extend an inline table")
		}
		child := n.fields[k]
		if child == nil {
			child = tomlObject(dotted)
			n.fields[k] = child
		}
		if dotted && (child.header || child.aot) {
			p.fail("cannot extend a header table by dotted key")
		}
		if child.kind == 'a' && child.aot {
			child = child.items[len(child.items)-1]
		}
		if child.kind != 'o' || child.inline {
			p.fail("key is already a value")
		}
		n = child
	}
	return n
}

func (p *tomlParser) table(root *tomlNode, keys []string, array bool) *tomlNode {
	if len(keys) > p.maxDepth {
		p.fail("table exceeds MaxDepth")
	}
	parent := p.descend(root, keys[:len(keys)-1], false)
	k := keys[len(keys)-1]
	child := parent.fields[k]
	if array {
		if child == nil {
			child = &tomlNode{kind: 'a', aot: true}
			parent.fields[k] = child
		}
		if child.kind != 'a' || !child.aot {
			p.fail("key is already a value or table")
		}
		n := tomlObject(true)
		child.items = append(child.items, n)
		return n
	}
	if child == nil {
		child = tomlObject(false)
		parent.fields[k] = child
	}
	if child.kind != 'o' || child.defined || child.inline {
		p.fail("table already defined")
	}
	child.defined = true
	child.header = true
	return child
}

func (p *tomlParser) assignment(now *tomlNode, path []string, depth int) {
	keys := p.keys()
	full := append(slices.Clone(path), keys...)
	p.key = strings.Join(full, ".")
	if depth+len(full) > p.maxDepth {
		p.fail("key exceeds MaxDepth")
	}
	p.expect('=')
	for p.peek() == ' ' || p.peek() == '\t' {
		p.next()
	}
	parent := p.descend(now, keys[:len(keys)-1], true)
	k := keys[len(keys)-1]
	if parent.fields[k] != nil {
		p.fail("key already defined")
	}
	parent.fields[k] = p.value(depth + len(full))
}

func (p *tomlParser) value(depth int) *tomlNode {
	if depth > p.maxDepth {
		p.fail("value exceeds MaxDepth")
	}
	switch p.peek() {
	case '"', '\'':
		return &tomlNode{kind: 's', text: p.quoted(true)}
	case '[':
		p.next()
		n := &tomlNode{kind: 'a'}
		p.space(true)
		for p.peek() != ']' {
			n.items = append(n.items, p.value(depth+1))
			p.space(true)
			if p.peek() != ',' {
				break
			}
			p.next()
			p.space(true)
		}
		p.expect(']')
		return n
	case '{':
		p.next()
		n := tomlObject(true)
		for p.peek() == ' ' || p.peek() == '\t' {
			p.next()
		}
		for p.peek() != '}' {
			key := p.key
			p.assignment(n, nil, depth+1)
			p.key = key
			for p.peek() == ' ' || p.peek() == '\t' {
				p.next()
			}
			if p.peek() != ',' {
				break
			}
			p.next()
			for p.peek() == ' ' || p.peek() == '\t' {
				p.next()
			}
			if p.peek() == '}' {
				p.fail("trailing comma in inline table")
			}
		}
		p.expect('}')
		n.inline = true
		return n
	default:
		return p.atom()
	}
}

func (p *tomlParser) quoted(allowMulti bool) string {
	q := p.next()
	multi := false
	if allowMulti && strings.HasPrefix(p.text[p.at:], string([]byte{q, q})) {
		p.next()
		p.next()
		multi = true
		if p.peek() == '\n' {
			p.next()
		}
	}
	var b strings.Builder
	for !p.end() {
		if p.peek() == q {
			count := 0
			for p.at+count < len(p.text) && p.text[p.at+count] == q {
				count++
			}
			if !multi {
				p.next()
				return b.String()
			}
			if count >= 3 {
				take := min(count, 5)
				b.WriteString(strings.Repeat(string(q), take-3))
				p.at += take
				return b.String()
			}
			for range count {
				b.WriteByte(p.next())
			}
			continue
		}
		c := p.next()
		if c == '\\' && q == '"' {
			if multi && (p.peek() == '\n' || p.peek() == ' ' || p.peek() == '\t') {
				for p.peek() == ' ' || p.peek() == '\t' {
					p.next()
				}
				p.expect('\n')
				for p.peek() == ' ' || p.peek() == '\t' || p.peek() == '\n' {
					p.next()
				}
				continue
			}
			e := p.next()
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
				width := 4
				if e == 'U' {
					width = 8
				}
				if len(p.text)-p.at < width {
					p.fail("incomplete Unicode escape")
				}
				x := p.text[p.at : p.at+width]
				for _, c := range x {
					if !strings.ContainsRune("0123456789abcdefABCDEF", c) {
						p.fail("invalid Unicode escape")
					}
				}
				v, err := strconv.ParseUint(x, 16, 32)
				if err != nil || v > utf8.MaxRune || v >= 0xd800 && v <= 0xdfff {
					p.fail("escape is not a Unicode scalar")
				}
				p.at += width
				b.WriteRune(rune(v))
			default:
				p.fail("unknown string escape")
			}
			continue
		}
		if c < 32 && c != '\t' && !(multi && c == '\n') || c == 127 {
			p.fail("control character in string")
		}
		b.WriteByte(c)
	}
	p.fail("unclosed string")
	return ""
}

// Grammar scanners have no package initialization work: importing another
// runtime facility must not retain a TOML parser or compile its grammar.
func tomlDigits(s, alphabet string) bool {
	if s == "" {
		return false
	}
	for i, c := range s {
		if c == '_' {
			if i == 0 || i == len(s)-1 || s[i-1] == '_' {
				return false
			}
		} else if !strings.ContainsRune(alphabet, c) {
			return false
		}
	}
	return true
}

func tomlUnsigned(s string) string {
	if strings.HasPrefix(s, "+") || strings.HasPrefix(s, "-") {
		return s[1:]
	}
	return s
}

func tomlDecimal(s string) bool {
	return tomlDigits(s, "0123456789") && (len(s) == 1 || s[0] != '0')
}

func tomlInteger(s string) (base int, digits string, ok bool) {
	base, digits, alphabet := 10, tomlUnsigned(s), "0123456789"
	if len(s) >= 2 && s[0] == '0' {
		switch s[1] {
		case 'x':
			base, alphabet = 16, "0123456789abcdefABCDEF"
		case 'o':
			base, alphabet = 8, "01234567"
		case 'b':
			base, alphabet = 2, "01"
		}
		if base != 10 {
			digits = s[2:]
		}
	}
	if !tomlDigits(digits, alphabet) {
		return 0, "", false
	}
	if base == 10 && !tomlDecimal(digits) {
		return 0, "", false
	}
	if base == 10 {
		digits = s
	}
	return base, digits, true
}

func tomlFloat(s string) bool {
	s = tomlUnsigned(s)
	mantissa, exponent, hasExponent := strings.Cut(strings.ToLower(s), "e")
	if hasExponent && !tomlDigits(tomlUnsigned(exponent), "0123456789") {
		return false
	}
	whole, fraction, hasFraction := strings.Cut(mantissa, ".")
	return (hasExponent || hasFraction) && tomlDecimal(whole) && (!hasFraction || tomlDigits(fraction, "0123456789"))
}

func tomlDate(s string) bool {
	return len(s) == 10 && s[4] == '-' && s[7] == '-' && tomlFixedDigits(s[:4]+s[5:7]+s[8:])
}

func tomlFixedDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return s != ""
}

func tomlTime(s string) bool {
	return len(s) >= 8 && s[2] == ':' && s[5] == ':' && tomlFixedDigits(s[:2]+s[3:5]+s[6:8]) && (len(s) == 8 || s[8] == '.' && tomlFixedDigits(s[9:]))
}

func tomlDateTime(s string) bool {
	if len(s) < 19 || !tomlDate(s[:10]) || !strings.ContainsRune("Tt ", rune(s[10])) {
		return false
	}
	tail := s[11:]
	if strings.HasSuffix(tail, "Z") || strings.HasSuffix(tail, "z") {
		tail = tail[:len(tail)-1]
	} else if len(tail) >= 14 && (tail[len(tail)-6] == '+' || tail[len(tail)-6] == '-') {
		off := tail[len(tail)-5:]
		if off[2] != ':' || !tomlFixedDigits(off[:2]+off[3:]) {
			return false
		}
		tail = tail[:len(tail)-6]
	}
	return tomlTime(tail)
}

func (p *tomlParser) atom() *tomlNode {
	start := p.at
	for !p.end() && !strings.ContainsRune(" \t\n,]}#", rune(p.peek())) {
		p.next()
	}
	// TOML permits a single space between the local date and time.
	if p.peek() == ' ' && p.at-start == 10 && tomlDate(p.text[start:p.at]) && p.at+1 < len(p.text) && p.text[p.at+1] >= '0' && p.text[p.at+1] <= '9' {
		p.next()
		for !p.end() && !strings.ContainsRune(" \t\n,]}#", rune(p.peek())) {
			p.next()
		}
	}
	s := p.text[start:p.at]
	plain := strings.ReplaceAll(s, "_", "")
	if s == "true" || s == "false" {
		return &tomlNode{kind: 'b', text: s}
	}
	if base, digits, ok := tomlInteger(s); ok {
		n, err := strconv.ParseInt(strings.ReplaceAll(digits, "_", ""), base, 64)
		if err != nil {
			p.fail("integer outside int64 range")
		}
		return &tomlNode{kind: 'n', text: strconv.FormatInt(n, 10)}
	}
	if s == "nan" || s == "+nan" || s == "-nan" {
		return &tomlNode{kind: 's', text: "NaN"}
	}
	if s == "inf" || s == "+inf" {
		return &tomlNode{kind: 's', text: "Infinity"}
	}
	if s == "-inf" {
		return &tomlNode{kind: 's', text: "-Infinity"}
	}
	if tomlFloat(s) {
		f, err := strconv.ParseFloat(plain, 64)
		if err != nil {
			p.fail("float outside float64 range")
		}
		text := strconv.FormatFloat(f, 'g', -1, 64)
		if !strings.ContainsAny(text, ".eE") {
			text += ".0"
		}
		return &tomlNode{kind: 'n', text: text}
	}
	layout := ""
	if tomlDate(s) {
		layout = "2006-01-02"
	}
	if tomlTime(s) {
		layout = "15:04:05.999999999"
	}
	if tomlDateTime(s) {
		s = s[:10] + "T" + s[11:]
		if strings.HasSuffix(s, "z") {
			s = strings.TrimSuffix(s, "z") + "Z"
		}
		layout = "2006-01-02T15:04:05.999999999"
		if strings.HasSuffix(s, "Z") || len(s) >= 6 && (s[len(s)-6] == '+' || s[len(s)-6] == '-') {
			layout = time.RFC3339Nano
			if !strings.HasSuffix(s, "Z") {
				off := s[len(s)-5:]
				if off[:2] > "23" || off[3:] > "59" {
					p.fail("invalid UTC offset")
				}
			}
		}
	}
	if layout != "" {
		v, err := time.Parse(layout, s)
		if err != nil {
			p.fail("invalid date or time")
		}
		return &tomlNode{kind: 's', text: v.Format(layout)}
	}
	p.fail("invalid value")
	return nil
}
