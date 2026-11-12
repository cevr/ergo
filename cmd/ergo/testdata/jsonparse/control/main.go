// Command control is the hand-written Go control of examples/jsonparse:
// the same recursive-descent parser over the whole text, the same value
// tree and the same printers, written as a Go programmer would write
// them: one tagged value struct, a byte switch for numbers, and
// strings.Builder output. It prints what the Ergo program prints, byte
// for byte, failures included.
//
//	control fmt FILE | min FILE | get POINTER FILE | check FILE
//	control CMD -flat ARGS    the same commands over a flat tree (nodes in
//	                          one slice, children as ranges of extras,
//	                          strings as spans), as jsonparse -flat
//	control check -std FILE   the same counts through encoding/json, for scale
//	stdjson check FILE        the same, through a link named stdjson
package main

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"
)

const usageText = "usage: jsonparse fmt [-flat] FILE | min [-flat] FILE | get [-flat] POINTER FILE | check [-flat] FILE | gen -seed S -n N"

const maxDepth = 10000

type kind uint8

const (
	kNull kind = iota
	kBool
	kNumber
	kString
	kArray
	kObject
)

// value is a JSON value: text holds a number's text or a string's bytes.
type value struct {
	kind    kind
	b       bool
	text    string
	items   []value
	members []member
}

type member struct {
	key string
	val value
}

// ---- parsing ----

type syntaxError struct {
	offset int
	reason string
}

type parser struct {
	src   string
	pos   int
	depth int
	err   *syntaxError
}

func (p *parser) fail(at int, reason string) {
	if p.err == nil {
		p.err = &syntaxError{at, reason}
	}
}

func (p *parser) found() string {
	if p.pos >= len(p.src) {
		return "the end"
	}
	return strconv.Quote(p.src[p.pos : p.pos+1])
}

func (p *parser) skip() {
	for p.pos < len(p.src) {
		switch p.src[p.pos] {
		case ' ', '\n', '\t', '\r':
			p.pos++
		default:
			return
		}
	}
}

func parse(src string) (value, *syntaxError) {
	p := &parser{src: src}
	p.skip()
	v := p.value()
	if p.err != nil {
		return value{}, p.err
	}
	p.skip()
	if p.pos < len(p.src) {
		p.fail(p.pos, "expected the end, found "+p.found())
		return value{}, p.err
	}
	return v, nil
}

func (p *parser) value() value {
	if p.pos >= len(p.src) {
		p.fail(p.pos, "expected a value, found the end")
		return value{}
	}
	switch c := p.src[p.pos]; c {
	case '{':
		return p.object()
	case '[':
		return p.array()
	case '"':
		s := p.str()
		return value{kind: kString, text: s}
	case 't':
		p.word("true")
		return value{kind: kBool, b: true}
	case 'f':
		p.word("false")
		return value{kind: kBool}
	case 'n':
		p.word("null")
		return value{}
	default:
		if c != '-' && (c < '0' || c > '9') {
			p.fail(p.pos, "expected a value, found "+p.found())
			return value{}
		}
		return p.number()
	}
}

func (p *parser) word(w string) {
	for k := 0; k < len(w); k++ {
		if p.pos >= len(p.src) || p.src[p.pos] != w[k] {
			p.fail(p.pos, "expected "+w+", found "+p.found())
			return
		}
		p.pos++
	}
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// number reads -? (0 | [1-9][0-9]*) (. [0-9]+)? ([eE] [+-]? [0-9]+)?
func (p *parser) number() value {
	s, i := p.src, p.pos
	start := i
	if s[i] == '-' {
		i++
	}
	switch {
	case i < len(s) && s[i] == '0':
		i++
	case i < len(s) && isDigit(s[i]):
		for i < len(s) && isDigit(s[i]) {
			i++
		}
	default:
		if i > start {
			p.fail(i, "a number needs a digit after '-'")
		} else {
			p.fail(i, "a number needs a digit")
		}
		return value{}
	}
	if i < len(s) && s[i] == '.' {
		i++
		if i >= len(s) || !isDigit(s[i]) {
			p.fail(i, "a number needs a digit after '.'")
			return value{}
		}
		for i < len(s) && isDigit(s[i]) {
			i++
		}
	}
	if i < len(s) && (s[i] == 'e' || s[i] == 'E') {
		i++
		if i < len(s) && (s[i] == '+' || s[i] == '-') {
			i++
		}
		if i >= len(s) || !isDigit(s[i]) {
			p.fail(i, "a number needs a digit in its exponent")
			return value{}
		}
		for i < len(s) && isDigit(s[i]) {
			i++
		}
	}
	p.pos = i
	return value{kind: kNumber, text: s[start:i]}
}

func (p *parser) enter() bool {
	p.depth++
	if p.depth > maxDepth {
		p.fail(p.pos, "nesting deeper than "+strconv.Itoa(maxDepth))
		return false
	}
	p.pos++
	p.skip()
	return true
}

func (p *parser) array() value {
	if !p.enter() {
		return value{}
	}
	items := []value{}
	if p.pos < len(p.src) && p.src[p.pos] == ']' {
		p.pos++
		p.depth--
		return value{kind: kArray, items: items}
	}
	for {
		v := p.value()
		if p.err != nil {
			return value{}
		}
		items = append(items, v)
		p.skip()
		if p.pos < len(p.src) {
			switch p.src[p.pos] {
			case ',':
				p.pos++
				p.skip()
				continue
			case ']':
				p.pos++
				p.depth--
				return value{kind: kArray, items: items}
			}
		}
		p.fail(p.pos, "expected ',' or ']', found "+p.found())
		return value{}
	}
}

func (p *parser) object() value {
	if !p.enter() {
		return value{}
	}
	members := []member{}
	if p.pos < len(p.src) && p.src[p.pos] == '}' {
		p.pos++
		p.depth--
		return value{kind: kObject, members: members}
	}
	for {
		if p.pos >= len(p.src) || p.src[p.pos] != '"' {
			p.fail(p.pos, "expected a string key, found "+p.found())
			return value{}
		}
		key := p.str()
		if p.err != nil {
			return value{}
		}
		p.skip()
		if p.pos >= len(p.src) || p.src[p.pos] != ':' {
			p.fail(p.pos, "expected ':', found "+p.found())
			return value{}
		}
		p.pos++
		p.skip()
		v := p.value()
		if p.err != nil {
			return value{}
		}
		members = append(members, member{key, v})
		p.skip()
		if p.pos < len(p.src) {
			switch p.src[p.pos] {
			case ',':
				p.pos++
				p.skip()
				continue
			case '}':
				p.pos++
				p.depth--
				return value{kind: kObject, members: members}
			}
		}
		p.fail(p.pos, "expected ',' or '}', found "+p.found())
		return value{}
	}
}

// str reads a string after its opening quote. A string with no escape
// and only ASCII is a slice of the source.
func (p *parser) str() string {
	s := p.src
	start := p.pos + 1
	i := start
	for i < len(s) {
		c := s[i]
		if c == '"' {
			p.pos = i + 1
			return s[start:i]
		}
		if c == '\\' || c < 0x20 || c >= 0x80 {
			break
		}
		i++
	}
	var b strings.Builder
	b.WriteString(s[start:i])
	for i < len(s) {
		c := s[i]
		switch {
		case c == '"':
			p.pos = i + 1
			return b.String()
		case c == '\\':
			i = p.escape(&b, i)
			if p.err != nil {
				return ""
			}
		case c < 0x20:
			p.fail(i, "a control character in a string")
			return ""
		case c < 0x80:
			b.WriteByte(c)
			i++
		default:
			r, size := utf8.DecodeRuneInString(s[i:])
			if r == utf8.RuneError && size == 1 {
				b.WriteString("\uFFFD")
			} else {
				b.WriteString(s[i : i+size])
			}
			i += size
		}
	}
	p.fail(len(s), "a string has no closing quote")
	return ""
}

func hexDigit(c byte) int {
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

// badHex is the offset of the first non-hex byte of the four at i, or -1.
func badHex(s string, i int) int {
	for k := i; k < i+4; k++ {
		if k >= len(s) {
			return len(s)
		}
		if hexDigit(s[k]) < 0 {
			return k
		}
	}
	return -1
}

func hex4(s string, i int) int {
	r := 0
	for k := i; k < i+4; k++ {
		r = r*16 + hexDigit(s[k])
	}
	return r
}

func (p *parser) escape(b *strings.Builder, i int) int {
	s := p.src
	if i+1 >= len(s) {
		p.fail(len(s), "a string has no closing quote")
		return i
	}
	switch s[i+1] {
	case '"':
		b.WriteByte('"')
	case '\\':
		b.WriteByte('\\')
	case '/':
		b.WriteByte('/')
	case 'b':
		b.WriteByte('\b')
	case 'f':
		b.WriteByte('\f')
	case 'n':
		b.WriteByte('\n')
	case 'r':
		b.WriteByte('\r')
	case 't':
		b.WriteByte('\t')
	case 'u':
		if bad := badHex(s, i+2); bad >= 0 {
			p.fail(bad, "a \\u escape needs four hex digits")
			return i
		}
		r := hex4(s, i+2)
		if r >= 0xD800 && r < 0xE000 {
			low := -1
			if i+7 < len(s) && s[i+6] == '\\' && s[i+7] == 'u' && badHex(s, i+8) < 0 {
				low = hex4(s, i+8)
			}
			if r < 0xDC00 && low >= 0xDC00 && low < 0xE000 {
				b.WriteRune(rune(0x10000 + (r-0xD800)*0x400 + (low - 0xDC00)))
				return i + 12
			}
			b.WriteString("\uFFFD")
			return i + 6
		}
		b.WriteRune(rune(r))
		return i + 6
	default:
		p.fail(i+1, "an unknown escape")
		return i
	}
	return i + 2
}

// ---- printing ----

const hexDigits = "0123456789abcdef"

func quote(b *strings.Builder, s string) {
	b.WriteByte('"')
	from := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 0x20 && c != '"' && c != '\\' && c != 0x7f {
			continue
		}
		b.WriteString(s[from:i])
		from = i + 1
		switch c {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\t':
			b.WriteString(`\t`)
		case '\r':
			b.WriteString(`\r`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		default:
			b.WriteString(`\u00`)
			b.WriteByte(hexDigits[c>>4])
			b.WriteByte(hexDigits[c&15])
		}
	}
	b.WriteString(s[from:])
	b.WriteByte('"')
}

func compact(b *strings.Builder, v *value) {
	switch v.kind {
	case kNull:
		b.WriteString("null")
	case kBool:
		if v.b {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case kNumber:
		b.WriteString(v.text)
	case kString:
		quote(b, v.text)
	case kArray:
		b.WriteByte('[')
		for i := range v.items {
			if i > 0 {
				b.WriteByte(',')
			}
			compact(b, &v.items[i])
		}
		b.WriteByte(']')
	case kObject:
		b.WriteByte('{')
		for i := range v.members {
			if i > 0 {
				b.WriteByte(',')
			}
			quote(b, v.members[i].key)
			b.WriteByte(':')
			compact(b, &v.members[i].val)
		}
		b.WriteByte('}')
	}
}

func newline(b *strings.Builder, depth int) {
	b.WriteByte('\n')
	for range depth {
		b.WriteString("  ")
	}
}

func indent(b *strings.Builder, v *value, depth int) {
	switch v.kind {
	case kArray:
		if len(v.items) == 0 {
			b.WriteString("[]")
			return
		}
		b.WriteByte('[')
		for i := range v.items {
			if i > 0 {
				b.WriteByte(',')
			}
			newline(b, depth+1)
			indent(b, &v.items[i], depth+1)
		}
		newline(b, depth)
		b.WriteByte(']')
	case kObject:
		if len(v.members) == 0 {
			b.WriteString("{}")
			return
		}
		b.WriteByte('{')
		for i := range v.members {
			if i > 0 {
				b.WriteByte(',')
			}
			newline(b, depth+1)
			quote(b, v.members[i].key)
			b.WriteString(": ")
			indent(b, &v.members[i].val, depth+1)
		}
		newline(b, depth)
		b.WriteByte('}')
	default:
		compact(b, v)
	}
}

// ---- pointers ----

func tokens(ptr string) ([]string, string) {
	if ptr == "" {
		return nil, ""
	}
	if ptr[0] != '/' {
		return nil, "a pointer starts with '/'"
	}
	raw := strings.Split(ptr[1:], "/")
	out := make([]string, 0, len(raw))
	for _, t := range raw {
		if !strings.Contains(t, "~") {
			out = append(out, t)
			continue
		}
		var b strings.Builder
		for i := 0; i < len(t); i++ {
			if t[i] != '~' {
				b.WriteByte(t[i])
				continue
			}
			if i+1 < len(t) && t[i+1] == '0' {
				b.WriteByte('~')
			} else if i+1 < len(t) && t[i+1] == '1' {
				b.WriteByte('/')
			} else {
				return nil, "a '~' is followed by 0 or 1"
			}
			i++
		}
		out = append(out, b.String())
	}
	return out, ""
}

func escaped(t string) string {
	return strings.ReplaceAll(strings.ReplaceAll(t, "~", "~0"), "/", "~1")
}

func index(t string) int {
	if len(t) == 0 || len(t) > 9 || (t[0] == '0' && len(t) > 1) {
		return -1
	}
	n := 0
	for i := 0; i < len(t); i++ {
		if !isDigit(t[i]) {
			return -1
		}
		n = n*10 + int(t[i]-'0')
	}
	return n
}

// get is the value at the tokens in v, or the prefix that names nothing.
func get(v *value, toks []string) (*value, string) {
	prefix := ""
	for _, t := range toks {
		prefix += "/" + escaped(t)
		var next *value
		switch v.kind {
		case kObject:
			for k := len(v.members) - 1; k >= 0; k-- {
				if v.members[k].key == t {
					next = &v.members[k].val
					break
				}
			}
		case kArray:
			if i := index(t); i >= 0 && i < len(v.items) {
				next = &v.items[i]
			}
		}
		if next == nil {
			return nil, prefix
		}
		v = next
	}
	return v, ""
}

func count(v *value) int {
	n := 1
	for i := range v.items {
		n += count(&v.items[i])
	}
	for i := range v.members {
		n += count(&v.members[i].val)
	}
	return n
}

func depth(v *value) int {
	if v.kind != kArray && v.kind != kObject {
		return 0
	}
	d := 0
	for i := range v.items {
		d = max(d, depth(&v.items[i]))
	}
	for i := range v.members {
		d = max(d, depth(&v.members[i].val))
	}
	return d + 1
}

// ---- encoding/json, for scale ----

func stdCount(v any) (int, int) {
	switch x := v.(type) {
	case []any:
		n, d := 1, 0
		for _, e := range x {
			c, e := stdCount(e)
			n, d = n+c, max(d, e)
		}
		return n, d + 1
	case map[string]any:
		n, d := 1, 0
		for _, e := range x {
			c, e := stdCount(e)
			n, d = n+c, max(d, e)
		}
		return n, d + 1
	}
	return 1, 0
}

// ---- the command ----

func die(line string) {
	os.Stderr.WriteString("jsonparse: " + line + "\n")
	os.Exit(1)
}

func readSource(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		reason := err.Error()
		switch {
		case errors.Is(err, fs.ErrNotExist):
			reason = "no such file"
		case errors.Is(err, fs.ErrPermission):
			reason = "permission denied"
		}
		die(path + ": " + reason)
	}
	return string(data)
}

func dieSyntax(path, src string, serr *syntaxError) {
	line, start := 1, 0
	for i := 0; i < serr.offset; i++ {
		if src[i] == '\n' {
			line++
			start = i + 1
		}
	}
	die(path + ":" + strconv.Itoa(line) + ":" + strconv.Itoa(serr.offset-start+1) + ": " + serr.reason)
}

func load(path string) (string, value) {
	src := readSource(path)
	v, serr := parse(src)
	if serr != nil {
		dieSyntax(path, src, serr)
	}
	return src, v
}

// ---- the flat tree (-flat) ----

// fnode is one value of a flat tree: for a number or a plain string the
// span [a, a+b) of the source; for a decoded string pool[a]; for an array
// b children at extras[a:]; for an object b members, key and value ids
// alternating at extras[a:].
type fnode struct {
	kind kind
	a, b int32
}

const kPooled = kObject + 1

type ftree struct {
	src    string
	nodes  []fnode
	extras []int32
	pool   []string
	root   int32
}

type fparser struct {
	parser
	t     ftree
	stack []int32
}

func (f *fparser) add(n fnode) int32 {
	f.t.nodes = append(f.t.nodes, n)
	return int32(len(f.t.nodes) - 1)
}

func parseFlat(src string) (ftree, *syntaxError) {
	f := &fparser{parser: parser{src: src}, t: ftree{src: src}}
	f.skip()
	root := f.fvalue()
	if f.err != nil {
		return ftree{}, f.err
	}
	f.skip()
	if f.pos < len(f.src) {
		f.fail(f.pos, "expected the end, found "+f.found())
		return ftree{}, f.err
	}
	f.t.root = root
	return f.t, nil
}

func (f *fparser) fvalue() int32 {
	if f.pos >= len(f.src) {
		f.fail(f.pos, "expected a value, found the end")
		return 0
	}
	switch c := f.src[f.pos]; c {
	case '{':
		return f.fcontainer(kObject, '}')
	case '[':
		return f.fcontainer(kArray, ']')
	case '"':
		return f.fstr()
	case 't', 'f', 'n':
		v := f.value()
		if f.err != nil {
			return 0
		}
		n := fnode{kind: v.kind}
		if v.b {
			n.b = 1
		}
		return f.add(n)
	default:
		start := f.pos
		f.value()
		if f.err != nil {
			return 0
		}
		return f.add(fnode{kind: kNumber, a: int32(start), b: int32(f.pos - start)})
	}
}

func (f *fparser) fstr() int32 {
	s, start := f.src, f.pos+1
	for i := start; i < len(s); i++ {
		c := s[i]
		if c == '"' {
			f.pos = i + 1
			return f.add(fnode{kind: kString, a: int32(start), b: int32(i - start)})
		}
		if c == '\\' || c < 0x20 || c >= 0x80 {
			break
		}
	}
	text := f.str()
	if f.err != nil {
		return 0
	}
	f.t.pool = append(f.t.pool, text)
	return f.add(fnode{kind: kPooled, a: int32(len(f.t.pool) - 1)})
}

// fcontainer reads an array or an object after its opening bracket.
func (f *fparser) fcontainer(kind kind, end byte) int32 {
	if !f.enter() {
		return 0
	}
	mark := len(f.stack)
	finish := func() int32 {
		f.pos++
		f.depth--
		start := int32(len(f.t.extras))
		f.t.extras = append(f.t.extras, f.stack[mark:]...)
		n := int32(len(f.stack) - mark)
		if kind == kObject {
			n /= 2
		}
		f.stack = f.stack[:mark]
		return f.add(fnode{kind: kind, a: start, b: n})
	}
	if f.pos < len(f.src) && f.src[f.pos] == end {
		return finish()
	}
	for {
		if kind == kObject {
			if f.pos >= len(f.src) || f.src[f.pos] != '"' {
				f.fail(f.pos, "expected a string key, found "+f.found())
				return 0
			}
			key := f.fstr()
			if f.err != nil {
				return 0
			}
			f.stack = append(f.stack, key)
			f.skip()
			if f.pos >= len(f.src) || f.src[f.pos] != ':' {
				f.fail(f.pos, "expected ':', found "+f.found())
				return 0
			}
			f.pos++
			f.skip()
		}
		v := f.fvalue()
		if f.err != nil {
			return 0
		}
		f.stack = append(f.stack, v)
		f.skip()
		if f.pos < len(f.src) {
			switch f.src[f.pos] {
			case ',':
				f.pos++
				f.skip()
				continue
			case end:
				return finish()
			}
		}
		if kind == kObject {
			f.fail(f.pos, "expected ',' or '}', found "+f.found())
		} else {
			f.fail(f.pos, "expected ',' or ']', found "+f.found())
		}
		return 0
	}
}

func (t *ftree) text(n fnode) string {
	if n.kind == kPooled {
		return t.pool[n.a]
	}
	return t.src[n.a : n.a+n.b]
}

func (t *ftree) compact(b *strings.Builder, id int32) {
	n := t.nodes[id]
	switch n.kind {
	case kNull:
		b.WriteString("null")
	case kBool:
		if n.b == 1 {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case kNumber:
		b.WriteString(t.text(n))
	case kString, kPooled:
		quote(b, t.text(n))
	case kArray:
		b.WriteByte('[')
		for k, c := range t.extras[n.a : n.a+n.b] {
			if k > 0 {
				b.WriteByte(',')
			}
			t.compact(b, c)
		}
		b.WriteByte(']')
	case kObject:
		b.WriteByte('{')
		for k := int32(0); k < n.b; k++ {
			if k > 0 {
				b.WriteByte(',')
			}
			quote(b, t.text(t.nodes[t.extras[n.a+2*k]]))
			b.WriteByte(':')
			t.compact(b, t.extras[n.a+2*k+1])
		}
		b.WriteByte('}')
	}
}

func (t *ftree) indent(b *strings.Builder, id int32, depth int) {
	n := t.nodes[id]
	if (n.kind != kArray && n.kind != kObject) || n.b == 0 {
		t.compact(b, id)
		return
	}
	open, close := byte('['), byte(']')
	if n.kind == kObject {
		open, close = '{', '}'
	}
	b.WriteByte(open)
	for k := int32(0); k < n.b; k++ {
		if k > 0 {
			b.WriteByte(',')
		}
		newline(b, depth+1)
		child := t.extras[n.a+k]
		if n.kind == kObject {
			quote(b, t.text(t.nodes[t.extras[n.a+2*k]]))
			b.WriteString(": ")
			child = t.extras[n.a+2*k+1]
		}
		t.indent(b, child, depth+1)
	}
	newline(b, depth)
	b.WriteByte(close)
}

func (t *ftree) get(toks []string) (int32, string) {
	id, prefix := t.root, ""
	for _, tok := range toks {
		prefix += "/" + escaped(tok)
		n := t.nodes[id]
		next := int32(-1)
		switch n.kind {
		case kObject:
			for k := n.b - 1; k >= 0; k-- {
				if t.text(t.nodes[t.extras[n.a+2*k]]) == tok {
					next = t.extras[n.a+2*k+1]
					break
				}
			}
		case kArray:
			if i := index(tok); i >= 0 && i < int(n.b) {
				next = t.extras[n.a+int32(i)]
			}
		}
		if next < 0 {
			return -1, prefix
		}
		id = next
	}
	return id, ""
}

func (t *ftree) count(id int32) int {
	n := t.nodes[id]
	c := 1
	switch n.kind {
	case kArray:
		for _, x := range t.extras[n.a : n.a+n.b] {
			c += t.count(x)
		}
	case kObject:
		for k := int32(0); k < n.b; k++ {
			c += t.count(t.extras[n.a+2*k+1])
		}
	}
	return c
}

func (t *ftree) depth(id int32) int {
	n := t.nodes[id]
	step := int32(1)
	switch n.kind {
	case kArray:
	case kObject:
		step = 2
	default:
		return 0
	}
	d := 0
	for k := int32(0); k < n.b; k++ {
		d = max(d, t.depth(t.extras[n.a+step*k+step-1]))
	}
	return d + 1
}

func loadFlat(path string) ftree {
	src := readSource(path)
	t, serr := parseFlat(src)
	if serr != nil {
		dieSyntax(path, src, serr)
	}
	return t
}

// mainFlat runs a command of -flat; args has the flag removed.
func mainFlat(args []string, b *strings.Builder) {
	switch {
	case args[0] == "fmt" && len(args) == 2:
		t := loadFlat(args[1])
		t.indent(b, t.root, 0)
	case args[0] == "min" && len(args) == 2:
		t := loadFlat(args[1])
		t.compact(b, t.root)
	case args[0] == "get" && len(args) == 3:
		t := loadFlat(args[2])
		toks, why := tokens(args[1])
		if why != "" {
			die(args[1] + ": " + why)
		}
		at, missing := t.get(toks)
		if at < 0 {
			die(args[2] + ": no value at " + missing)
		}
		t.indent(b, at, 0)
	case args[0] == "check" && len(args) == 2:
		t := loadFlat(args[1])
		b.WriteString("values " + strconv.Itoa(t.count(t.root)) + "\ndepth " + strconv.Itoa(t.depth(t.root)))
	default:
		usage()
	}
}

func usage() {
	os.Stderr.WriteString(usageText + "\n")
	os.Exit(2)
}

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		usage()
	}
	if filepath.Base(os.Args[0]) == "stdjson" && len(args) == 2 && args[0] == "check" {
		// Called as stdjson (a link to this binary), check is
		// encoding/json's, so the receipts run both sides with one command.
		args = []string{"check", "-std", args[1]}
	}
	var b strings.Builder
	if len(args) >= 2 && args[1] == "-flat" {
		mainFlat(append([]string{args[0]}, args[2:]...), &b)
		b.WriteByte('\n')
		os.Stdout.WriteString(b.String())
		return
	}
	switch {
	case args[0] == "fmt" && len(args) == 2:
		_, v := load(args[1])
		indent(&b, &v, 0)
	case args[0] == "min" && len(args) == 2:
		_, v := load(args[1])
		compact(&b, &v)
	case args[0] == "get" && len(args) == 3:
		_, v := load(args[2])
		toks, why := tokens(args[1])
		if why != "" {
			die(args[1] + ": " + why)
		}
		at, missing := get(&v, toks)
		if at == nil {
			die(args[2] + ": no value at " + missing)
		}
		indent(&b, at, 0)
	case args[0] == "check" && len(args) == 2:
		_, v := load(args[1])
		b.WriteString("values " + strconv.Itoa(count(&v)) + "\ndepth " + strconv.Itoa(depth(&v)))
	case args[0] == "check" && len(args) == 3 && args[1] == "-std":
		data, err := os.ReadFile(args[2])
		if err != nil {
			die(args[2] + ": " + err.Error())
		}
		dec := json.NewDecoder(strings.NewReader(string(data)))
		dec.UseNumber()
		var v any
		if err := dec.Decode(&v); err != nil {
			die(args[2] + ": " + err.Error())
		}
		n, d := stdCount(v)
		b.WriteString("values " + strconv.Itoa(n) + "\ndepth " + strconv.Itoa(d))
	default:
		usage()
	}
	b.WriteByte('\n')
	os.Stdout.WriteString(b.String())
}
