// Command control is the hand Go control of examples/tomlparse: a TOML 1.0
// parser of the same design (a node store with each key's definition
// state, then a tree) and the same commands, output and failure lines.
//
//	control json FILE             FILE as toml-test's tagged JSON
//	control fmt FILE              FILE printed again as TOML
//	control check FILE            how many values FILE holds and how deep they nest
//
// A failure prints "tomlparse: FILE:LINE:COL: reason" and exits 1; a usage
// error exits 2.
package main

import (
	"errors"
	"io/fs"
	"math"
	"os"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

const usageText = "usage: tomlparse json FILE | fmt FILE | check FILE | gen -seed S -n N"

const maxDepth = 10000

// ---- values ----

type kind uint8

const (
	kStr kind = iota
	kInt
	kFloat
	kBool
	kMoment
	kArray
	kTable
)

type clock uint8

const (
	offsetDateTime clock = iota
	localDateTime
	localDate
	localTime
)

type value struct {
	kind  kind
	s     string // a string, or a moment's text
	n     int64
	f     float64
	b     bool
	clock clock
	items []*value
	keys  []string // a table's keys; items are its values
}

func count(v *value) int {
	n := 1
	for _, x := range v.items {
		n += count(x)
	}
	return n
}

func depth(v *value) int {
	if v.kind != kArray && v.kind != kTable {
		return 0
	}
	d := 0
	for _, x := range v.items {
		d = max(d, depth(x))
	}
	return d + 1
}

// ---- the definition machine ----

type shape uint8

const (
	absent shape = iota
	implicit
	header
	dotted
	tables
	closed
)

type use uint8

const (
	pass use = iota
	open
	appendTo
	dot
	set
)

// step is the next shape, or why the use is refused ("" and twice false
// when it is not). twice is a key defined twice.
func step(s shape, u use) (next shape, twice bool, why string) {
	if s == absent {
		switch u {
		case pass:
			return implicit, false, ""
		case open:
			return header, false, ""
		case appendTo:
			return tables, false, ""
		case dot:
			return dotted, false, ""
		default:
			return closed, false, ""
		}
	}
	if u == set {
		return s, true, ""
	}
	switch {
	case s != closed && u == pass, s == tables && u == appendTo, s == dotted && u == dot:
		return s, false, ""
	case s == implicit && u == open:
		return header, false, ""
	case s == implicit && u == dot:
		return dotted, false, ""
	case s == header && u == open:
		return s, true, ""
	case (s == implicit || s == header || s == dotted) && u == appendTo:
		return s, false, "is a table, not an array of tables"
	case s == header && u == dot:
		return s, false, "is a table a header defined, which dotted keys cannot extend"
	case s == dotted && u == open:
		return s, false, "is a table dotted keys defined, which a header cannot define"
	case s == tables && u == open:
		return s, false, "is an array of tables, not a table"
	case s == tables && u == dot:
		return s, false, "is an array of tables, which dotted keys cannot extend"
	}
	return s, false, "is a value, so nothing can be added to it"
}

// ---- the parser ----

type form uint8

const (
	leaf form = iota
	items
	keys
	elements
)

type node struct {
	parent int
	name   string
	shape  shape
	form   form
	val    *value
	last   int
}

type slot struct {
	parent int
	name   string
}

type seg struct {
	name string
	at   int
}

// failure is a parse failure: the offset and the line after "FILE:L:C: ".
type failure struct {
	at   int
	line string
}

type builder struct {
	src   string
	pos   int
	depth int
	nodes []node
	index map[slot]int
	table int
	path  string
	err   *failure
}

func (b *builder) fail(at int, reason string) {
	if b.err == nil {
		b.err = &failure{at: at, line: reason}
	}
}

func (b *builder) found(at int) string {
	if at >= len(b.src) {
		return "the end"
	}
	return strconv.Quote(b.src[at : at+1])
}

func parse(src string) (*value, *failure) {
	b := &builder{src: src, index: map[slot]int{}}
	for i := 0; i < len(src); {
		if src[i] < 0x80 {
			i++
			continue
		}
		_, size := utf8.DecodeRuneInString(src[i:])
		if size == 1 {
			b.fail(i, "a byte that is not UTF-8")
			return nil, b.err
		}
		i += size
	}
	if strings.HasPrefix(src, "\uFEFF") {
		b.pos = 3
	}
	b.nodes = append(b.nodes, node{parent: -1, shape: header, form: keys})
	for b.err == nil {
		b.blank()
		if b.pos >= len(b.src) {
			break
		}
		c := b.src[b.pos]
		if c == '[' {
			b.header()
		} else if c != '#' && c != '\n' && c != '\r' {
			b.keyval(b.table, b.path)
		}
		if b.err == nil {
			b.endLine()
		}
	}
	if b.err != nil {
		return nil, b.err
	}
	return b.tree(), nil
}

func (b *builder) blank() {
	for b.pos < len(b.src) && (b.src[b.pos] == ' ' || b.src[b.pos] == '\t') {
		b.pos++
	}
}

func control(c byte) bool { return (c < 0x20 && c != '\t') || c == 0x7f }

func (b *builder) comment() {
	b.pos++
	for b.pos < len(b.src) {
		c := b.src[b.pos]
		if c == '\n' || (c == '\r' && b.pos+1 < len(b.src) && b.src[b.pos+1] == '\n') {
			return
		}
		if control(c) {
			b.fail(b.pos, "a control character in a comment")
			return
		}
		b.pos++
	}
}

func (b *builder) newline() bool {
	if b.pos < len(b.src) && b.src[b.pos] == '\n' {
		b.pos++
		return true
	}
	if b.pos+1 < len(b.src) && b.src[b.pos] == '\r' && b.src[b.pos+1] == '\n' {
		b.pos += 2
		return true
	}
	return false
}

func (b *builder) endLine() {
	b.blank()
	if b.pos < len(b.src) && b.src[b.pos] == '#' {
		if b.comment(); b.err != nil {
			return
		}
	}
	if b.pos >= len(b.src) || b.newline() {
		return
	}
	b.fail(b.pos, "expected the end of the line, found "+b.found(b.pos))
}

func bare(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-'
}

func (b *builder) key() []seg {
	var segs []seg
	for {
		at := b.pos
		if b.pos >= len(b.src) {
			b.fail(b.pos, "expected a key, found the end")
			return nil
		}
		c := b.src[b.pos]
		switch {
		case c == '"':
			if strings.HasPrefix(b.src[b.pos:], `"""`) {
				b.fail(b.pos, "a key cannot be a multi-line string")
				return nil
			}
			name := b.basic()
			if b.err != nil {
				return nil
			}
			segs = append(segs, seg{name, at})
		case c == '\'':
			if strings.HasPrefix(b.src[b.pos:], "'''") {
				b.fail(b.pos, "a key cannot be a multi-line string")
				return nil
			}
			name := b.literal()
			if b.err != nil {
				return nil
			}
			segs = append(segs, seg{name, at})
		case bare(c):
			i := b.pos
			for i < len(b.src) && bare(b.src[i]) {
				i++
			}
			segs = append(segs, seg{b.src[b.pos:i], at})
			b.pos = i
		default:
			b.fail(b.pos, "expected a key, found "+b.found(b.pos))
			return nil
		}
		b.blank()
		if b.pos >= len(b.src) || b.src[b.pos] != '.' {
			return segs
		}
		b.pos++
		b.blank()
	}
}

func shown(name string) string {
	if name == "" {
		return `""`
	}
	for i := 0; i < len(name); i++ {
		if !bare(name[i]) {
			return quoted(name)
		}
	}
	return name
}

func joined(path, name string) string {
	if path == "" {
		return shown(name)
	}
	return path + "." + shown(name)
}

func (b *builder) add(parent int, name string, s shape, f form) int {
	b.nodes = append(b.nodes, node{parent: parent, name: name, shape: s, form: f})
	return len(b.nodes) - 1
}

// locate is "L:C: " of offset at.
func (b *builder) locate(at int) string {
	line, start := 1, 0
	for i := 0; i < at; i++ {
		if b.src[i] == '\n' {
			line, start = line+1, i+1
		}
	}
	return strconv.Itoa(line) + ":" + strconv.Itoa(at-start+1) + ": "
}

func (b *builder) touch(parent int, sg seg, u use, path string) int {
	at := slot{parent, sg.name}
	id, ok := b.index[at]
	s := absent
	if ok {
		s = b.nodes[id].shape
	} else {
		id = -1
	}
	next, twice, why := step(s, u)
	switch {
	case twice:
		b.err = &failure{at: sg.at, line: "key " + path + " is defined twice"}
		return 0
	case why != "":
		b.err = &failure{at: sg.at, line: "key " + path + " " + why}
		return 0
	case id < 0:
		f := keys
		switch u {
		case appendTo:
			f = elements
		case set:
			f = leaf
		}
		id = b.add(parent, sg.name, next, f)
		b.index[at] = id
	default:
		b.nodes[id].shape = next
	}
	switch u {
	case appendTo:
		t := b.add(id, "", header, keys)
		b.nodes[id].last = t
		return t
	case pass:
		if b.nodes[id].form == elements {
			return b.nodes[id].last
		}
	}
	return id
}

func (b *builder) header() {
	tbl := b.pos+1 < len(b.src) && b.src[b.pos+1] == '['
	if tbl {
		b.pos += 2
	} else {
		b.pos++
	}
	b.blank()
	segs := b.key()
	if b.err != nil {
		return
	}
	end := "]"
	if tbl {
		end = "]]"
	}
	if !strings.HasPrefix(b.src[b.pos:], end) {
		b.fail(b.pos, "expected '"+end+"', found "+b.found(b.pos))
		return
	}
	b.pos += len(end)
	at, path := 0, ""
	for i, sg := range segs {
		path = joined(path, sg.name)
		u := open
		if i < len(segs)-1 {
			u = pass
		} else if tbl {
			u = appendTo
		}
		if at = b.touch(at, sg, u, path); b.err != nil {
			return
		}
	}
	b.table, b.path = at, path
}

func (b *builder) keyval(table int, path string) {
	segs := b.key()
	if b.err != nil {
		return
	}
	if b.pos >= len(b.src) || b.src[b.pos] != '=' {
		b.fail(b.pos, "expected '=' after a key, found "+b.found(b.pos))
		return
	}
	b.pos++
	b.blank()
	at := table
	for i, sg := range segs {
		path = joined(path, sg.name)
		u := set
		if i < len(segs)-1 {
			u = dot
		}
		if at = b.touch(at, sg, u, path); b.err != nil {
			return
		}
	}
	b.value(at, path)
}

func (b *builder) value(id int, path string) {
	if b.pos >= len(b.src) {
		b.fail(b.pos, "expected a value, found the end")
		return
	}
	switch c := b.src[b.pos]; {
	case c == '"':
		s := b.basic()
		b.nodes[id].val = &value{kind: kStr, s: s}
	case c == '\'':
		s := b.literal()
		b.nodes[id].val = &value{kind: kStr, s: s}
	case c == '[':
		b.array(id, path)
	case c == '{':
		b.inline(id, path)
	case c == 't':
		b.word("true")
		b.nodes[id].val = &value{kind: kBool, b: true}
	case c == 'f':
		b.word("false")
		b.nodes[id].val = &value{kind: kBool}
	case c == '+' || c == '-' || c == 'i' || c == 'n' || (c >= '0' && c <= '9'):
		b.nodes[id].val = b.scalar()
	default:
		b.fail(b.pos, "expected a value, found "+b.found(b.pos))
	}
}

func (b *builder) word(w string) {
	if !strings.HasPrefix(b.src[b.pos:], w) {
		b.fail(b.pos, "expected a value, found "+b.found(b.pos))
		return
	}
	b.pos += len(w)
	b.ended(b.pos)
}

func (b *builder) ended(at int) {
	if at >= len(b.src) {
		return
	}
	switch b.src[at] {
	case ' ', '\t', '\n', '\r', ',', ']', '}', '#':
	default:
		b.fail(at, "expected the end of a value, found "+b.found(at))
	}
}

func (b *builder) enter() bool {
	b.depth++
	if b.depth > maxDepth {
		b.fail(b.pos, "nesting deeper than "+strconv.Itoa(maxDepth))
		return false
	}
	b.pos++
	return true
}

func (b *builder) fill() {
	for {
		b.blank()
		if b.pos < len(b.src) && b.src[b.pos] == '#' {
			if b.comment(); b.err != nil {
				return
			}
		}
		if !b.newline() {
			return
		}
	}
}

func (b *builder) array(id int, path string) {
	if !b.enter() {
		return
	}
	b.nodes[id].form = items
	for {
		if b.fill(); b.err != nil {
			return
		}
		if b.pos < len(b.src) && b.src[b.pos] == ']' {
			b.pos++
			b.depth--
			return
		}
		item := b.add(id, "", closed, leaf)
		if b.value(item, path); b.err != nil {
			return
		}
		if b.fill(); b.err != nil {
			return
		}
		if b.pos < len(b.src) && b.src[b.pos] == ',' {
			b.pos++
			continue
		}
		if b.pos < len(b.src) && b.src[b.pos] == ']' {
			b.pos++
			b.depth--
			return
		}
		b.fail(b.pos, "expected ',' or ']', found "+b.found(b.pos))
		return
	}
}

func (b *builder) inline(id int, path string) {
	if !b.enter() {
		return
	}
	b.nodes[id].form = keys
	b.blank()
	if b.pos < len(b.src) && b.src[b.pos] == '}' {
		b.pos++
		b.depth--
		return
	}
	for {
		if b.keyval(id, path); b.err != nil {
			return
		}
		b.blank()
		if b.pos < len(b.src) && b.src[b.pos] == ',' {
			b.pos++
			b.blank()
			continue
		}
		if b.pos < len(b.src) && b.src[b.pos] == '}' {
			b.pos++
			b.depth--
			return
		}
		b.fail(b.pos, "expected ',' or '}', found "+b.found(b.pos))
		return
	}
}

func (b *builder) tree() *value {
	n := len(b.nodes)
	first := make([]int, n+1)
	for id := 1; id < n; id++ {
		first[b.nodes[id].parent+1]++
	}
	for k := 0; k < n; k++ {
		first[k+1] += first[k]
	}
	next := slices.Clone(first)
	kids := make([]int, n)
	for id := 1; id < n; id++ {
		p := b.nodes[id].parent
		kids[next[p]] = id
		next[p]++
	}
	var build func(id int) *value
	build = func(id int) *value {
		nd := b.nodes[id]
		switch nd.form {
		case leaf:
			return nd.val
		case keys:
			v := &value{kind: kTable}
			for _, k := range kids[first[id]:first[id+1]] {
				v.keys = append(v.keys, b.nodes[k].name)
				v.items = append(v.items, build(k))
			}
			return v
		}
		v := &value{kind: kArray}
		for _, k := range kids[first[id]:first[id+1]] {
			v.items = append(v.items, build(k))
		}
		return v
	}
	return build(0)
}

// ---- strings ----

func (b *builder) basic() string {
	if strings.HasPrefix(b.src[b.pos:], `"""`) {
		return b.multiBasic()
	}
	start := b.pos + 1
	i := start
	for i < len(b.src) {
		c := b.src[i]
		if c == '"' {
			b.pos = i + 1
			return b.src[start:i]
		}
		if c == '\\' || control(c) {
			break
		}
		i++
	}
	var out strings.Builder
	out.WriteString(b.src[start:i])
	for i < len(b.src) {
		c := b.src[i]
		switch {
		case c == '"':
			b.pos = i + 1
			return out.String()
		case c == '\\':
			if i = b.escape(&out, i); b.err != nil {
				return ""
			}
		case c == '\n' || c == '\r':
			b.fail(i, "a newline in a one-line string")
			return ""
		case control(c):
			b.fail(i, "a control character in a string")
			return ""
		default:
			out.WriteByte(c)
			i++
		}
	}
	b.fail(len(b.src), "a string has no closing quote")
	return ""
}

func (b *builder) escape(out *strings.Builder, i int) int {
	if i+1 >= len(b.src) {
		b.fail(len(b.src), "a string has no closing quote")
		return i
	}
	switch b.src[i+1] {
	case '"':
		out.WriteByte('"')
	case '\\':
		out.WriteByte('\\')
	case 'b':
		out.WriteByte(0x08)
	case 'f':
		out.WriteByte(0x0c)
	case 'n':
		out.WriteByte('\n')
	case 'r':
		out.WriteByte('\r')
	case 't':
		out.WriteByte('\t')
	case 'u':
		out.WriteString(b.scalarEscape(i, 4))
		return i + 6
	case 'U':
		out.WriteString(b.scalarEscape(i, 8))
		return i + 10
	default:
		b.fail(i, "an unknown escape")
		return i
	}
	return i + 2
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

func (b *builder) scalarEscape(i, n int) string {
	r := 0
	for k := i + 2; k < i+2+n; k++ {
		if k >= len(b.src) || hexDigit(b.src[k]) < 0 {
			letter := "u"
			if n == 8 {
				letter = "U"
			}
			b.fail(k, `a \`+letter+" escape needs "+strconv.Itoa(n)+" hex digits")
			return ""
		}
		r = r*16 + hexDigit(b.src[k])
	}
	if (r >= 0xD800 && r < 0xE000) || r > 0x10FFFF {
		b.fail(i, "an escape of a code point that is not a Unicode scalar value")
		return ""
	}
	return string(rune(r))
}

func (b *builder) quotes(i int, q byte) int {
	k := i
	for k < len(b.src) && b.src[k] == q {
		k++
	}
	return k - i
}

func (b *builder) multiBasic() string {
	b.pos += 3
	b.newline()
	var out strings.Builder
	i := b.pos
	for i < len(b.src) {
		c := b.src[i]
		switch {
		case c == '"':
			q := b.quotes(i, '"')
			if q >= 3 {
				if q > 5 {
					b.fail(i+5, "too many quotes at the end of a multi-line string")
					return ""
				}
				out.WriteString(strings.Repeat(`"`, q-3))
				b.pos = i + q
				return out.String()
			}
			out.WriteString(strings.Repeat(`"`, q))
			i += q
		case c == '\\' && i+1 < len(b.src) && strings.IndexByte(" \t\n\r", b.src[i+1]) >= 0:
			k := i + 1
			for k < len(b.src) && (b.src[k] == ' ' || b.src[k] == '\t') {
				k++
			}
			b.pos = k
			if !b.newline() {
				b.fail(k, "a backslash ends a line only before its newline")
				return ""
			}
			k = b.pos
			for k < len(b.src) {
				b.pos = k
				if b.src[k] == ' ' || b.src[k] == '\t' {
					k++
				} else if b.newline() {
					k = b.pos
				} else {
					break
				}
			}
			i = k
		case c == '\\':
			if i = b.escape(&out, i); b.err != nil {
				return ""
			}
		case c == '\n':
			out.WriteByte('\n')
			i++
		case c == '\r' && i+1 < len(b.src) && b.src[i+1] == '\n':
			out.WriteString("\r\n")
			i += 2
		case control(c):
			b.fail(i, "a control character in a string")
			return ""
		default:
			out.WriteByte(c)
			i++
		}
	}
	b.fail(len(b.src), "a multi-line string has no closing quotes")
	return ""
}

func (b *builder) literal() string {
	if strings.HasPrefix(b.src[b.pos:], "'''") {
		return b.multiLiteral()
	}
	start := b.pos + 1
	for i := start; i < len(b.src); i++ {
		c := b.src[i]
		if c == '\'' {
			b.pos = i + 1
			return b.src[start:i]
		}
		if c == '\n' || c == '\r' {
			b.fail(i, "a newline in a one-line string")
			return ""
		}
		if control(c) {
			b.fail(i, "a control character in a string")
			return ""
		}
	}
	b.fail(len(b.src), "a string has no closing quote")
	return ""
}

func (b *builder) multiLiteral() string {
	b.pos += 3
	b.newline()
	var out strings.Builder
	i := b.pos
	for i < len(b.src) {
		c := b.src[i]
		switch {
		case c == '\'':
			q := b.quotes(i, '\'')
			if q >= 3 {
				if q > 5 {
					b.fail(i+5, "too many quotes at the end of a multi-line string")
					return ""
				}
				out.WriteString(strings.Repeat("'", q-3))
				b.pos = i + q
				return out.String()
			}
			out.WriteString(strings.Repeat("'", q))
			i += q
		case c == '\n':
			out.WriteByte('\n')
			i++
		case c == '\r' && i+1 < len(b.src) && b.src[i+1] == '\n':
			out.WriteString("\r\n")
			i += 2
		case control(c):
			b.fail(i, "a control character in a string")
			return ""
		default:
			out.WriteByte(c)
			i++
		}
	}
	b.fail(len(b.src), "a multi-line string has no closing quotes")
	return ""
}

// ---- numbers, dates and times ----

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func (b *builder) digitsAt(i, n int) bool {
	if i+n > len(b.src) {
		return false
	}
	for k := i; k < i+n; k++ {
		if !isDigit(b.src[k]) {
			return false
		}
	}
	return true
}

func (b *builder) scalar() *value {
	if b.digitsAt(b.pos, 4) && b.pos+4 < len(b.src) && b.src[b.pos+4] == '-' {
		return b.moment()
	}
	if b.digitsAt(b.pos, 2) && b.pos+2 < len(b.src) && b.src[b.pos+2] == ':' {
		return b.moment()
	}
	return b.numeral()
}

func ofBase(c byte, base int) bool {
	switch base {
	case 16:
		return hexDigit(c) >= 0
	case 8:
		return c >= '0' && c <= '7'
	case 2:
		return c == '0' || c == '1'
	}
	return isDigit(c)
}

func (b *builder) run(i, base int, out *strings.Builder) int {
	if i >= len(b.src) || !ofBase(b.src[i], base) {
		b.fail(i, "expected a digit, found "+b.found(i))
		return i
	}
	out.WriteByte(b.src[i])
	j := i + 1
	for j < len(b.src) {
		c := b.src[j]
		if ofBase(c, base) {
			out.WriteByte(c)
			j++
		} else if c == '_' {
			if j+1 >= len(b.src) || !ofBase(b.src[j+1], base) {
				b.fail(j, "an underscore needs a digit on each side")
				return j
			}
			j++
		} else {
			break
		}
	}
	return j
}

func (b *builder) numeral() *value {
	start := b.pos
	i := start
	var out strings.Builder
	if b.src[i] == '+' || b.src[i] == '-' {
		if b.src[i] == '-' {
			out.WriteByte('-')
		}
		i++
	}
	rest := b.src[i:]
	if strings.HasPrefix(rest, "inf") || strings.HasPrefix(rest, "nan") {
		if b.ended(i + 3); b.err != nil {
			return nil
		}
		b.pos = i + 3
		if strings.HasPrefix(rest, "nan") {
			return &value{kind: kFloat, f: math.NaN()}
		}
		sign := 1
		if b.src[start] == '-' {
			sign = -1
		}
		return &value{kind: kFloat, f: math.Inf(sign)}
	}
	if i == start && len(rest) > 1 && rest[0] == '0' && (rest[1] == 'x' || rest[1] == 'o' || rest[1] == 'b') {
		base := map[byte]int{'x': 16, 'o': 8, 'b': 2}[rest[1]]
		end := b.run(i+2, base, &out)
		if b.err != nil {
			return nil
		}
		if b.ended(end); b.err != nil {
			return nil
		}
		b.pos = end
		n, err := strconv.ParseInt(out.String(), base, 64)
		if err != nil {
			b.fail(start, "an integer out of range")
			return nil
		}
		return &value{kind: kInt, n: n}
	}
	whole := i
	if i = b.run(i, 10, &out); b.err != nil {
		return nil
	}
	if b.src[whole] == '0' && i > whole+1 {
		b.fail(whole, "a number with a leading zero")
		return nil
	}
	float := false
	if i < len(b.src) && b.src[i] == '.' {
		out.WriteByte('.')
		if i = b.run(i+1, 10, &out); b.err != nil {
			return nil
		}
		float = true
	}
	if i < len(b.src) && (b.src[i] == 'e' || b.src[i] == 'E') {
		out.WriteByte('e')
		i++
		if i < len(b.src) && (b.src[i] == '+' || b.src[i] == '-') {
			out.WriteByte(b.src[i])
			i++
		}
		if i = b.run(i, 10, &out); b.err != nil {
			return nil
		}
		float = true
	}
	if b.ended(i); b.err != nil {
		return nil
	}
	b.pos = i
	if float {
		f, err := strconv.ParseFloat(out.String(), 64)
		if err != nil {
			b.fail(start, "a float out of range")
			return nil
		}
		return &value{kind: kFloat, f: f}
	}
	n, err := strconv.ParseInt(out.String(), 10, 64)
	if err != nil {
		b.fail(start, "an integer out of range")
		return nil
	}
	return &value{kind: kInt, n: n}
}

func (b *builder) two(i int) int { return int(b.src[i]-'0')*10 + int(b.src[i+1]-'0') }

func days(year, month int) int {
	switch month {
	case 2:
		if year%4 == 0 && (year%100 != 0 || year%400 == 0) {
			return 29
		}
		return 28
	case 4, 6, 9, 11:
		return 30
	}
	return 31
}

func (b *builder) clock(i int, out *strings.Builder) int {
	if !b.digitsAt(i, 2) || i+2 >= len(b.src) || b.src[i+2] != ':' || !b.digitsAt(i+3, 2) || i+5 >= len(b.src) || b.src[i+5] != ':' || !b.digitsAt(i+6, 2) {
		b.fail(i, "a time is HH:MM:SS")
		return i
	}
	if b.two(i) > 23 || b.two(i+3) > 59 || b.two(i+6) > 59 {
		b.fail(i, "a time out of range")
		return i
	}
	j := i + 8
	if j < len(b.src) && b.src[j] == '.' {
		if !b.digitsAt(j+1, 1) {
			b.fail(j+1, "expected a digit, found "+b.found(j+1))
			return j
		}
		j++
		for j < len(b.src) && isDigit(b.src[j]) {
			j++
		}
	}
	out.WriteString(b.src[i:j])
	return j
}

func (b *builder) moment() *value {
	start := b.pos
	var out strings.Builder
	if b.src[start+2] == ':' {
		end := b.clock(start, &out)
		if b.err != nil {
			return nil
		}
		if b.ended(end); b.err != nil {
			return nil
		}
		b.pos = end
		return &value{kind: kMoment, clock: localTime, s: out.String()}
	}
	if !b.digitsAt(start, 4) || b.src[start+4] != '-' || !b.digitsAt(start+5, 2) || start+7 >= len(b.src) || b.src[start+7] != '-' || !b.digitsAt(start+8, 2) {
		b.fail(start, "a date is YYYY-MM-DD")
		return nil
	}
	year := b.two(start)*100 + b.two(start+2)
	month, day := b.two(start+5), b.two(start+8)
	if month < 1 || month > 12 || day < 1 || day > days(year, month) {
		b.fail(start, "a date out of range")
		return nil
	}
	out.WriteString(b.src[start : start+10])
	i := start + 10
	spaced := i+1 < len(b.src) && b.src[i] == ' ' && isDigit(b.src[i+1])
	if i >= len(b.src) || (b.src[i] != 'T' && b.src[i] != 't' && !spaced) {
		if b.ended(i); b.err != nil {
			return nil
		}
		b.pos = i
		return &value{kind: kMoment, clock: localDate, s: out.String()}
	}
	out.WriteByte('T')
	if i = b.clock(i+1, &out); b.err != nil {
		return nil
	}
	c := localDateTime
	if i < len(b.src) && (b.src[i] == 'Z' || b.src[i] == 'z') {
		out.WriteByte('Z')
		i++
		c = offsetDateTime
	} else if i < len(b.src) && (b.src[i] == '+' || b.src[i] == '-') {
		if !b.digitsAt(i+1, 2) || i+3 >= len(b.src) || b.src[i+3] != ':' || !b.digitsAt(i+4, 2) {
			b.fail(i, "an offset is +HH:MM or -HH:MM")
			return nil
		}
		if b.two(i+1) > 23 || b.two(i+4) > 59 {
			b.fail(i, "an offset out of range")
			return nil
		}
		out.WriteString(b.src[i : i+6])
		i += 6
		c = offsetDateTime
	}
	if b.ended(i); b.err != nil {
		return nil
	}
	b.pos = i
	return &value{kind: kMoment, clock: c, s: out.String()}
}

// ---- printers ----

func writeQuoted(b *strings.Builder, s string) {
	const hex = "0123456789abcdef"
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		c := s[i]
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
		case 0x08:
			b.WriteString(`\b`)
		case 0x0c:
			b.WriteString(`\f`)
		default:
			if c < 0x20 || c == 0x7f {
				b.WriteString(`\u00`)
				b.WriteByte(hex[c>>4])
				b.WriteByte(hex[c&15])
			} else {
				b.WriteByte(c)
			}
		}
	}
	b.WriteByte('"')
}

func quoted(s string) string {
	var b strings.Builder
	writeQuoted(&b, s)
	return b.String()
}

func floatText(f float64) string {
	switch {
	case math.IsNaN(f):
		return "nan"
	case math.IsInf(f, 1):
		return "inf"
	case math.IsInf(f, -1):
		return "-inf"
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}

func tag(b *strings.Builder, kind, text string) {
	b.WriteString(`{"type":"` + kind + `","value":`)
	writeQuoted(b, text)
	b.WriteByte('}')
}

func tagged(b *strings.Builder, v *value) {
	switch v.kind {
	case kStr:
		tag(b, "string", v.s)
	case kInt:
		tag(b, "integer", strconv.FormatInt(v.n, 10))
	case kFloat:
		tag(b, "float", floatText(v.f))
	case kBool:
		tag(b, "bool", strconv.FormatBool(v.b))
	case kMoment:
		tag(b, [...]string{"datetime", "datetime-local", "date-local", "time-local"}[v.clock], v.s)
	case kArray:
		b.WriteByte('[')
		for i, x := range v.items {
			if i > 0 {
				b.WriteByte(',')
			}
			tagged(b, x)
		}
		b.WriteByte(']')
	case kTable:
		order := make([]int, len(v.keys))
		for i := range order {
			order[i] = i
		}
		slices.SortStableFunc(order, func(x, y int) int { return strings.Compare(v.keys[x], v.keys[y]) })
		b.WriteByte('{')
		for i, k := range order {
			if i > 0 {
				b.WriteByte(',')
			}
			writeQuoted(b, v.keys[k])
			b.WriteByte(':')
			tagged(b, v.items[k])
		}
		b.WriteByte('}')
	}
}

func sections(v *value) bool {
	if v.kind == kTable {
		return true
	}
	if v.kind != kArray || len(v.items) == 0 {
		return false
	}
	for _, x := range v.items {
		if x.kind != kTable {
			return false
		}
	}
	return true
}

func section(b *strings.Builder, t *value, path string) {
	for i, x := range t.items {
		if !sections(x) {
			b.WriteString(shown(t.keys[i]) + " = ")
			inlineValue(b, x)
			b.WriteByte('\n')
		}
	}
	for i, x := range t.items {
		if !sections(x) {
			continue
		}
		inner := joined(path, t.keys[i])
		if x.kind == kTable {
			b.WriteString("\n[" + inner + "]\n")
			section(b, x, inner)
			continue
		}
		for _, sub := range x.items {
			b.WriteString("\n[[" + inner + "]]\n")
			section(b, sub, inner)
		}
	}
}

func inlineValue(b *strings.Builder, v *value) {
	switch v.kind {
	case kStr:
		writeQuoted(b, v.s)
	case kInt:
		b.WriteString(strconv.FormatInt(v.n, 10))
	case kFloat:
		text := floatText(v.f)
		b.WriteString(text)
		if !strings.ContainsAny(text, ".en") {
			b.WriteString(".0")
		}
	case kBool:
		b.WriteString(strconv.FormatBool(v.b))
	case kMoment:
		b.WriteString(v.s)
	case kArray:
		b.WriteByte('[')
		for i, x := range v.items {
			if i > 0 {
				b.WriteString(", ")
			}
			inlineValue(b, x)
		}
		b.WriteByte(']')
	case kTable:
		b.WriteByte('{')
		for i, x := range v.items {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(shown(v.keys[i]) + " = ")
			inlineValue(b, x)
		}
		b.WriteByte('}')
	}
}

// ---- the commands ----

func die(line string) {
	os.Stderr.WriteString("tomlparse: " + line + "\n")
	os.Exit(1)
}

func usage() {
	os.Stderr.WriteString(usageText + "\n")
	os.Exit(2)
}

func load(path string) *value {
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
	v, f := parse(src)
	if f != nil {
		b := builder{src: src}
		die(path + ":" + b.locate(f.at) + f.line)
	}
	return v
}

func main() {
	args := os.Args[1:]
	if len(args) != 2 {
		usage()
	}
	var b strings.Builder
	switch args[0] {
	case "json":
		tagged(&b, load(args[1]))
		b.WriteByte('\n')
	case "fmt":
		v := load(args[1])
		section(&b, v, "")
	case "check":
		v := load(args[1])
		b.WriteString("values " + strconv.Itoa(count(v)) + "\ndepth " + strconv.Itoa(depth(v)) + "\n")
	default:
		usage()
	}
	os.Stdout.WriteString(b.String())
}
