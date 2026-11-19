// Command control is the hand-written Go control of examples/flattree:
// the same single-pass parser into the same flat tree, written as a Go
// programmer would write the dense form: hand columns (kind, a, b) sized
// from the text, an array's items and an object's members as a span of
// two pools held in the node itself, keys interned in a map to indexes
// of one slice of texts, the scratch stacks truncated and reused, and the
// stats in one pass over the rows in id order. It prints what the Ergo
// program prints, byte for byte, failures included.
//
//	control stats FILE | path FILE POINTER | print FILE
package main

import (
	"errors"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"
)

const usageText = "usage: flattree stats [-tree] FILE | path [-tree] FILE POINTER | print [-tree] FILE | gen -seed S -n N"

const maxDepth = 10000

// The kinds of a node, and what a and b hold for each.
const (
	kNull   = iota // nothing
	kFalse         // nothing
	kTrue          // nothing
	kNumber        // the span src[a : a+b]
	kSpan          // a string: the span src[a : a+b]
	kText          // a string: texts[a]
	kArray         // b items at items[a:]
	kObject        // b members at members[a:]
)

type member struct{ key, value uint32 }

// tree is a document read flat: node i is kind[i], a[i], b[i].
type tree struct {
	src     string
	kind    []uint8
	a, b    []uint32
	items   []uint32
	members []member
	keys    map[string]uint32
	texts   []string
}

func (t *tree) add(kind uint8, a, b uint32) uint32 {
	t.kind = append(t.kind, kind)
	t.a = append(t.a, a)
	t.b = append(t.b, b)
	return uint32(len(t.kind) - 1)
}

func (t *tree) intern(s string) uint32 {
	if id, ok := t.keys[s]; ok {
		return id
	}
	id := uint32(len(t.texts))
	t.texts = append(t.texts, s)
	t.keys[s] = id
	return id
}

func (t *tree) root() uint32 { return uint32(len(t.kind) - 1) }

// ---- parsing ----

type syntaxError struct {
	offset int
	reason string
}

type parser struct {
	t     tree
	src   string
	pos   int
	depth int
	err   *syntaxError
	stack []uint32
	held  []member
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
	s, i := p.src, p.pos
	for i < len(s) {
		switch s[i] {
		case ' ', '\n', '\t', '\r':
			i++
		default:
			p.pos = i
			return
		}
	}
	p.pos = i
}

func parse(src string) (tree, *syntaxError) {
	// About one node per 6 bytes of a typical document; append grows the
	// rest.
	n := len(src)/6 + 8
	p := &parser{
		src: src,
		t: tree{
			src: src, kind: make([]uint8, 0, n), a: make([]uint32, 0, n), b: make([]uint32, 0, n),
			items: make([]uint32, 0, n/2), members: make([]member, 0, n/2), keys: map[string]uint32{},
		},
		stack: make([]uint32, 0, 64),
		held:  make([]member, 0, 64),
	}
	p.skip()
	p.value()
	if p.err != nil {
		return tree{}, p.err
	}
	p.skip()
	if p.pos < len(p.src) {
		p.fail(p.pos, "expected the end, found "+p.found())
		return tree{}, p.err
	}
	return p.t, nil
}

// value reads one value and leaves its id on the stack.
func (p *parser) value() {
	if p.pos >= len(p.src) {
		p.fail(p.pos, "expected a value, found the end")
		return
	}
	switch c := p.src[p.pos]; c {
	case '{':
		p.object()
	case '[':
		p.array()
	case '"':
		kind, a, b := p.str()
		if p.err == nil {
			p.stack = append(p.stack, p.t.add(kind, a, b))
		}
	case 't':
		p.word("true", kTrue)
	case 'f':
		p.word("false", kFalse)
	case 'n':
		p.word("null", kNull)
	default:
		if c != '-' && (c < '0' || c > '9') {
			p.fail(p.pos, "expected a value, found "+p.found())
			return
		}
		p.number()
	}
}

func (p *parser) word(w string, kind uint8) {
	for k := 0; k < len(w); k++ {
		if p.pos >= len(p.src) || p.src[p.pos] != w[k] {
			p.fail(p.pos, "expected "+w+", found "+p.found())
			return
		}
		p.pos++
	}
	p.stack = append(p.stack, p.t.add(kind, 0, 0))
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// number reads -? (0 | [1-9][0-9]*) (. [0-9]+)? ([eE] [+-]? [0-9]+)?
func (p *parser) number() {
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
		return
	}
	if i < len(s) && s[i] == '.' {
		i++
		if i >= len(s) || !isDigit(s[i]) {
			p.fail(i, "a number needs a digit after '.'")
			return
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
			return
		}
		for i < len(s) && isDigit(s[i]) {
			i++
		}
	}
	p.pos = i
	p.stack = append(p.stack, p.t.add(kNumber, uint32(start), uint32(i-start)))
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

func (p *parser) array() {
	if !p.enter() {
		return
	}
	mark := len(p.stack)
	if p.pos < len(p.src) && p.src[p.pos] == ']' {
		p.pos++
		p.depth--
		p.stack = append(p.stack, p.t.add(kArray, uint32(len(p.t.items)), 0))
		return
	}
	for {
		p.value()
		if p.err != nil {
			return
		}
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
				start := uint32(len(p.t.items))
				p.t.items = append(p.t.items, p.stack[mark:]...)
				n := uint32(len(p.stack) - mark)
				p.stack = append(p.stack[:mark], p.t.add(kArray, start, n))
				return
			}
		}
		p.fail(p.pos, "expected ',' or ']', found "+p.found())
		return
	}
}

func (p *parser) object() {
	if !p.enter() {
		return
	}
	mark := len(p.held)
	if p.pos < len(p.src) && p.src[p.pos] == '}' {
		p.pos++
		p.depth--
		p.stack = append(p.stack, p.t.add(kObject, uint32(len(p.t.members)), 0))
		return
	}
	for {
		if p.pos >= len(p.src) || p.src[p.pos] != '"' {
			p.fail(p.pos, "expected a string key, found "+p.found())
			return
		}
		kind, a, b := p.str()
		if p.err != nil {
			return
		}
		key := a
		if kind == kSpan {
			key = p.t.intern(p.src[a : a+b])
		}
		p.skip()
		if p.pos >= len(p.src) || p.src[p.pos] != ':' {
			p.fail(p.pos, "expected ':', found "+p.found())
			return
		}
		p.pos++
		p.skip()
		p.value()
		if p.err != nil {
			return
		}
		top := len(p.stack) - 1
		p.held = append(p.held, member{key, p.stack[top]})
		p.stack = p.stack[:top]
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
				start := uint32(len(p.t.members))
				p.t.members = append(p.t.members, p.held[mark:]...)
				n := uint32(len(p.held) - mark)
				p.held = p.held[:mark]
				p.stack = append(p.stack, p.t.add(kObject, start, n))
				return
			}
		}
		p.fail(p.pos, "expected ',' or '}', found "+p.found())
		return
	}
}

// str reads a string literal: a span of the source when it has no escape
// and only ASCII, else its decoded text, interned.
func (p *parser) str() (kind uint8, a, b uint32) {
	s := p.src
	start := p.pos + 1
	i := start
	for i < len(s) {
		c := s[i]
		if c == '"' {
			p.pos = i + 1
			return kSpan, uint32(start), uint32(i - start)
		}
		if c == '\\' || c < 0x20 || c >= 0x80 {
			break
		}
		i++
	}
	var sb strings.Builder
	sb.WriteString(s[start:i])
	for i < len(s) {
		c := s[i]
		switch {
		case c == '"':
			p.pos = i + 1
			return kText, p.t.intern(sb.String()), 0
		case c == '\\':
			i = p.escape(&sb, i)
			if p.err != nil {
				return 0, 0, 0
			}
		case c < 0x20:
			p.fail(i, "a control character in a string")
			return 0, 0, 0
		case c < 0x80:
			sb.WriteByte(c)
			i++
		default:
			r, size := utf8.DecodeRuneInString(s[i:])
			if r == utf8.RuneError && size == 1 {
				sb.WriteString("\uFFFD")
			} else {
				sb.WriteString(s[i : i+size])
			}
			i += size
		}
	}
	p.fail(len(s), "a string has no closing quote")
	return 0, 0, 0
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

// text is the string node id holds.
func (t *tree) text(id uint32) string {
	if t.kind[id] == kText {
		return t.texts[t.a[id]]
	}
	a := t.a[id]
	return t.src[a : a+t.b[id]]
}

func (t *tree) compact(b *strings.Builder, id uint32) {
	a, n := t.a[id], t.b[id]
	switch t.kind[id] {
	case kNull:
		b.WriteString("null")
	case kFalse:
		b.WriteString("false")
	case kTrue:
		b.WriteString("true")
	case kNumber:
		b.WriteString(t.src[a : a+n])
	case kSpan, kText:
		quote(b, t.text(id))
	case kArray:
		b.WriteByte('[')
		for k, c := range t.items[a : a+n] {
			if k > 0 {
				b.WriteByte(',')
			}
			t.compact(b, c)
		}
		b.WriteByte(']')
	case kObject:
		b.WriteByte('{')
		for k, m := range t.members[a : a+n] {
			if k > 0 {
				b.WriteByte(',')
			}
			quote(b, t.texts[m.key])
			b.WriteByte(':')
			t.compact(b, m.value)
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

func (t *tree) indent(b *strings.Builder, id uint32, depth int) {
	a, n := t.a[id], t.b[id]
	switch {
	case t.kind[id] == kArray && n > 0:
		b.WriteByte('[')
		for k, c := range t.items[a : a+n] {
			if k > 0 {
				b.WriteByte(',')
			}
			newline(b, depth+1)
			t.indent(b, c, depth+1)
		}
		newline(b, depth)
		b.WriteByte(']')
	case t.kind[id] == kObject && n > 0:
		b.WriteByte('{')
		for k, m := range t.members[a : a+n] {
			if k > 0 {
				b.WriteByte(',')
			}
			newline(b, depth+1)
			quote(b, t.texts[m.key])
			b.WriteString(": ")
			t.indent(b, m.value, depth+1)
		}
		newline(b, depth)
		b.WriteByte('}')
	default:
		t.compact(b, id)
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

// get is the id at the tokens, or the prefix that names nothing. A token
// is looked up once among the keys; members match by key id.
func (t *tree) get(toks []string) (uint32, string, bool) {
	id, prefix := t.root(), ""
	for _, tok := range toks {
		prefix += "/" + escaped(tok)
		a, n := t.a[id], t.b[id]
		found := false
		switch t.kind[id] {
		case kObject:
			key, ok := t.keys[tok]
			if !ok {
				break
			}
			ms := t.members[a : a+n]
			for k := len(ms) - 1; k >= 0; k-- {
				if ms[k].key == key {
					id, found = ms[k].value, true
					break
				}
			}
		case kArray:
			if i := index(tok); i >= 0 && i < int(n) {
				id, found = t.items[a+uint32(i)], true
			}
		}
		if !found {
			return 0, prefix, false
		}
	}
	return id, "", true
}

// stats is one pass over the rows in id order: a child comes before its
// parent, so its depth is known when the parent's is taken.
func (t *tree) stats(b *strings.Builder) {
	var counts [8]int
	depth := make([]int32, len(t.kind))
	seen := make([]bool, len(t.texts))
	keys := 0
	for i, k := range t.kind {
		counts[k]++
		a, n := t.a[i], t.b[i]
		switch k {
		case kArray:
			d := int32(0)
			for _, c := range t.items[a : a+n] {
				d = max(d, depth[c])
			}
			depth[i] = d + 1
		case kObject:
			d := int32(0)
			for _, m := range t.members[a : a+n] {
				d = max(d, depth[m.value])
				if !seen[m.key] {
					seen[m.key] = true
					keys++
				}
			}
			depth[i] = d + 1
		}
	}
	line := func(name string, n int) {
		b.WriteString(name)
		b.WriteByte(' ')
		b.WriteString(strconv.Itoa(n))
		b.WriteByte('\n')
	}
	line("values", len(t.kind))
	line("depth", int(depth[t.root()]))
	line("null", counts[kNull])
	line("bool", counts[kFalse]+counts[kTrue])
	line("number", counts[kNumber])
	line("string", counts[kSpan]+counts[kText])
	line("array", counts[kArray])
	line("object", counts[kObject])
	line("keys", keys)
}

// ---- the command ----

func die(line string) {
	os.Stderr.WriteString("flattree: " + line + "\n")
	os.Exit(1)
}

func load(path string) tree {
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
	src := string(data)
	t, serr := parse(src)
	if serr != nil {
		line, start := 1, 0
		for i := 0; i < serr.offset; i++ {
			if src[i] == '\n' {
				line++
				start = i + 1
			}
		}
		die(path + ":" + strconv.Itoa(line) + ":" + strconv.Itoa(serr.offset-start+1) + ": " + serr.reason)
	}
	return t
}

func usage() {
	os.Stderr.WriteString(usageText + "\n")
	os.Exit(2)
}

func main() {
	args := os.Args[1:]
	var b strings.Builder
	switch {
	case len(args) == 2 && args[0] == "stats":
		t := load(args[1])
		t.stats(&b)
	case len(args) == 3 && args[0] == "path":
		t := load(args[1])
		toks, why := tokens(args[2])
		if why != "" {
			die(args[2] + ": " + why)
		}
		at, missing, ok := t.get(toks)
		if !ok {
			die(args[1] + ": no value at " + missing)
		}
		t.indent(&b, at, 0)
		b.WriteByte('\n')
	case len(args) == 2 && args[0] == "print":
		t := load(args[1])
		t.compact(&b, t.root())
		b.WriteByte('\n')
	default:
		usage()
	}
	os.Stdout.WriteString(b.String())
}
