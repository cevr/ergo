package main

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The inline phase: the text of a paragraph or a heading into a tree of
// inlines, held in one slice and linked by ids, with CommonMark's
// delimiter stack for emphasis and its bracket stack for links.

type inlKind uint8

const (
	iRoot inlKind = iota
	iText
	iSoft
	iHard
	iCode
	iEmph
	iStrong
	iLink
	iImage
	iHTML
)

type inline struct {
	kind                inlKind
	text                string // a text's, code span's or raw HTML's literal
	dest, title         string
	parent, first, last int
	prev, next          int
}

type delim struct {
	cc                byte
	num, orig         int
	node              int
	prev, next        int
	canOpen, canClose bool
}

type bracket struct {
	node         int
	prevDelim    int
	index        int
	image        bool
	active       bool
	bracketAfter bool
}

type inlineParser struct {
	subject  string
	pos      int
	nodes    []inline
	delims   []delim
	top      int // the top of the delimiter stack, or -1
	brackets []bracket
	refs     map[string]linkRef
}

func (q *inlineParser) newNode(k inlKind, text string) int {
	q.nodes = append(q.nodes, inline{kind: k, text: text, parent: -1, first: -1, last: -1, prev: -1, next: -1})
	return len(q.nodes) - 1
}

func (q *inlineParser) appendChild(parent, child int) {
	n := &q.nodes[child]
	n.parent = parent
	n.prev = q.nodes[parent].last
	n.next = -1
	if n.prev >= 0 {
		q.nodes[n.prev].next = child
	} else {
		q.nodes[parent].first = child
	}
	q.nodes[parent].last = child
}

func (q *inlineParser) unlink(id int) {
	n := q.nodes[id]
	if n.prev >= 0 {
		q.nodes[n.prev].next = n.next
	} else if n.parent >= 0 {
		q.nodes[n.parent].first = n.next
	}
	if n.next >= 0 {
		q.nodes[n.next].prev = n.prev
	} else if n.parent >= 0 {
		q.nodes[n.parent].last = n.prev
	}
	q.nodes[id].parent, q.nodes[id].prev, q.nodes[id].next = -1, -1, -1
}

// insertAfter puts the unlinked node id right after at.
func (q *inlineParser) insertAfter(at, id int) {
	next := q.nodes[at].next
	parent := q.nodes[at].parent
	q.nodes[id].parent, q.nodes[id].prev, q.nodes[id].next = parent, at, next
	q.nodes[at].next = id
	if next >= 0 {
		q.nodes[next].prev = id
	} else if parent >= 0 {
		q.nodes[parent].last = id
	}
}

func (q *inlineParser) text(parent int, s string) {
	q.appendChild(parent, q.newNode(iText, s))
}

func (q *inlineParser) peek() int {
	if q.pos < len(q.subject) {
		return int(q.subject[q.pos])
	}
	return -1
}

// parse reads content's inlines under a new root, and is the root.
func (q *inlineParser) parse(content string) int {
	q.subject = trimSpace(content)
	q.pos = 0
	q.delims = q.delims[:0]
	q.top = -1
	q.brackets = q.brackets[:0]
	root := q.newNode(iRoot, "")
	for q.parseInline(root) {
	}
	q.processEmphasis(-1)
	return root
}

func (q *inlineParser) parseInline(root int) bool {
	c := q.peek()
	if c == -1 {
		return false
	}
	ok := false
	switch c {
	case '\n':
		ok = q.newline(root)
	case '\\':
		ok = q.backslash(root)
	case '`':
		ok = q.backticks(root)
	case '*', '_':
		ok = q.handleDelim(byte(c), root)
	case '[':
		q.pos++
		id := q.newNode(iText, "[")
		q.appendChild(root, id)
		q.addBracket(id, q.pos-1, false)
		ok = true
	case '!':
		q.pos++
		if q.peek() == '[' {
			q.pos++
			id := q.newNode(iText, "![")
			q.appendChild(root, id)
			q.addBracket(id, q.pos-1, true)
		} else {
			q.text(root, "!")
		}
		ok = true
	case ']':
		ok = q.closeBracket(root)
	case '<':
		ok = q.autolink(root) || q.htmlTag(root)
	case '&':
		ok = q.entity(root)
	default:
		ok = q.str(root)
	}
	if !ok {
		q.pos++
		q.text(root, string(rune(c)))
	}
	return true
}

func special(c byte) bool {
	switch c {
	case '\n', '`', '[', ']', '\\', '!', '<', '&', '*', '_', '\'', '"':
		return true
	}
	return false
}

func (q *inlineParser) str(root int) bool {
	start := q.pos
	for q.pos < len(q.subject) && !special(q.subject[q.pos]) {
		q.pos++
	}
	if q.pos == start {
		return false
	}
	q.text(root, q.subject[start:q.pos])
	return true
}

func (q *inlineParser) newline(root int) bool {
	q.pos++
	last := q.nodes[root].last
	if last >= 0 && q.nodes[last].kind == iText && strings.HasSuffix(q.nodes[last].text, " ") {
		t := q.nodes[last].text
		hard := len(t) >= 2 && t[len(t)-2] == ' '
		q.nodes[last].text = strings.TrimRight(t, " ")
		if hard {
			q.appendChild(root, q.newNode(iHard, ""))
		} else {
			q.appendChild(root, q.newNode(iSoft, ""))
		}
	} else {
		q.appendChild(root, q.newNode(iSoft, ""))
	}
	for q.peek() == ' ' {
		q.pos++
	}
	return true
}

func escapable(c byte) bool {
	return strings.IndexByte("!\"#$%&'()*+,./:;<=>?@[\\]^_`{|}~-", c) >= 0
}

func (q *inlineParser) backslash(root int) bool {
	q.pos++
	c := q.peek()
	if c == '\n' {
		q.pos++
		q.appendChild(root, q.newNode(iHard, ""))
	} else if c >= 0 && escapable(byte(c)) {
		q.text(root, string(byte(c)))
		q.pos++
	} else {
		q.text(root, "\\")
	}
	return true
}

func (q *inlineParser) backticks(root int) bool {
	start := q.pos
	for q.peek() == '`' {
		q.pos++
	}
	ticks := q.pos - start
	after := q.pos
	for q.pos < len(q.subject) {
		if q.subject[q.pos] != '`' {
			q.pos++
			continue
		}
		run := q.pos
		for q.peek() == '`' {
			q.pos++
		}
		if q.pos-run == ticks {
			contents := strings.ReplaceAll(q.subject[after:run], "\n", " ")
			if len(contents) >= 2 && contents[0] == ' ' && contents[len(contents)-1] == ' ' && strings.Trim(contents, " ") != "" {
				contents = contents[1 : len(contents)-1]
			}
			q.appendChild(root, q.newNode(iCode, contents))
			return true
		}
	}
	q.pos = after
	q.text(root, q.subject[start:after])
	return true
}

// runeBefore and runeAfter are the characters around a delimiter run; a
// line ending stands for the start or end of the text.
func (q *inlineParser) runeBefore(i int) rune {
	if i == 0 {
		return '\n'
	}
	from := i - 1
	for from > 0 && from > i-4 && q.subject[from] >= 0x80 && q.subject[from] < 0xC0 {
		from--
	}
	r, size := utf8.DecodeRuneInString(q.subject[from:i])
	if size != i-from {
		return utf8.RuneError
	}
	return r
}

func (q *inlineParser) runeAfter(i int) rune {
	if i >= len(q.subject) {
		return '\n'
	}
	r, _ := utf8.DecodeRuneInString(q.subject[i:])
	return r
}

// unicodeSpace is CommonMark's Unicode whitespace: Zs, tab, line feed,
// form feed, carriage return.
func unicodeSpace(r rune) bool {
	switch r {
	case ' ', '\t', '\n', '\f', '\r', 0xA0, 0x1680, 0x202F, 0x205F, 0x3000:
		return true
	}
	return r >= 0x2000 && r <= 0x200A
}

// punctuation is CommonMark's Unicode punctuation: categories P and S.
func punctuation(r rune) bool { return unicode.IsPunct(r) || unicode.IsSymbol(r) }

func (q *inlineParser) handleDelim(cc byte, root int) bool {
	start := q.pos
	for q.peek() == int(cc) {
		q.pos++
	}
	n := q.pos - start
	before, after := q.runeBefore(start), q.runeAfter(q.pos)
	afterSpace, afterPunct := unicodeSpace(after), punctuation(after)
	beforeSpace, beforePunct := unicodeSpace(before), punctuation(before)
	left := !afterSpace && (!afterPunct || beforeSpace || beforePunct)
	right := !beforeSpace && (!beforePunct || afterSpace || afterPunct)
	canOpen, canClose := left, right
	if cc == '_' {
		canOpen = left && (!right || beforePunct)
		canClose = right && (!left || afterPunct)
	}
	id := q.newNode(iText, q.subject[start:q.pos])
	q.appendChild(root, id)
	if canOpen || canClose {
		q.delims = append(q.delims, delim{cc: cc, num: n, orig: n, node: id, prev: q.top, next: -1, canOpen: canOpen, canClose: canClose})
		d := len(q.delims) - 1
		if q.top >= 0 {
			q.delims[q.top].next = d
		}
		q.top = d
	}
	return true
}

func (q *inlineParser) removeDelim(d int) {
	x := q.delims[d]
	if x.prev >= 0 {
		q.delims[x.prev].next = x.next
	}
	if x.next < 0 {
		q.top = x.prev
	} else {
		q.delims[x.next].prev = x.prev
	}
}

func (q *inlineParser) processEmphasis(bottom int) {
	var openersBottom [12]int
	for i := range openersBottom {
		openersBottom[i] = bottom
	}
	closer := q.top
	for closer >= 0 && q.delims[closer].prev != bottom {
		closer = q.delims[closer].prev
	}
	for closer >= 0 {
		c := q.delims[closer]
		if !c.canClose {
			closer = c.next
			continue
		}
		idx := c.orig % 3
		if c.canOpen {
			idx += 3
		}
		if c.cc == '*' {
			idx += 6
		}
		opener := c.prev
		found := false
		for opener >= 0 && opener != bottom && opener != openersBottom[idx] {
			o := q.delims[opener]
			odd := (c.canOpen || o.canClose) && c.orig%3 != 0 && (o.orig+c.orig)%3 == 0
			if o.cc == c.cc && o.canOpen && !odd {
				found = true
				break
			}
			opener = o.prev
		}
		old := closer
		if !found {
			closer = c.next
		} else {
			use := 1
			if c.num >= 2 && q.delims[opener].num >= 2 {
				use = 2
			}
			on, cn := q.delims[opener].node, c.node
			q.delims[opener].num -= use
			q.delims[closer].num -= use
			q.nodes[on].text = q.nodes[on].text[:len(q.nodes[on].text)-use]
			q.nodes[cn].text = q.nodes[cn].text[:len(q.nodes[cn].text)-use]
			k := iEmph
			if use == 2 {
				k = iStrong
			}
			emph := q.newNode(k, "")
			for t := q.nodes[on].next; t >= 0 && t != cn; {
				next := q.nodes[t].next
				q.unlink(t)
				q.appendChild(emph, t)
				t = next
			}
			q.insertAfter(on, emph)
			// remove the delimiters between opener and closer
			if q.delims[opener].next != closer {
				q.delims[opener].next = closer
				q.delims[closer].prev = opener
			}
			if q.delims[opener].num == 0 {
				q.unlink(on)
				q.removeDelim(opener)
			}
			if q.delims[closer].num == 0 {
				q.unlink(cn)
				next := q.delims[closer].next
				q.removeDelim(closer)
				closer = next
			}
		}
		if !found {
			openersBottom[idx] = q.delims[old].prev
			if !q.delims[old].canOpen {
				q.removeDelim(old)
			}
		}
	}
	for q.top >= 0 && q.top != bottom {
		q.removeDelim(q.top)
	}
}

func (q *inlineParser) addBracket(node, index int, image bool) {
	if n := len(q.brackets); n > 0 {
		q.brackets[n-1].bracketAfter = true
	}
	q.brackets = append(q.brackets, bracket{node: node, prevDelim: q.top, index: index, image: image, active: true})
}

func (q *inlineParser) closeBracket(root int) bool {
	q.pos++
	start := q.pos
	if len(q.brackets) == 0 {
		q.text(root, "]")
		return true
	}
	opener := q.brackets[len(q.brackets)-1]
	if !opener.active {
		q.text(root, "]")
		q.brackets = q.brackets[:len(q.brackets)-1]
		return true
	}
	dest, title := "", ""
	matched := false
	save := q.pos
	if q.peek() == '(' {
		q.pos++
		q.spnl()
		if d, ok := q.linkDestination(); ok {
			dest = d
			q.spnl()
			if q.pos > 0 && isSpace(q.subject[q.pos-1]) {
				if t, ok := q.linkTitle(); ok {
					title = t
				}
			}
			q.spnl()
			if q.peek() == ')' {
				q.pos++
				matched = true
			}
		}
		if !matched {
			q.pos = save
		}
	}
	if !matched {
		before := q.pos
		n := q.linkLabel()
		label := ""
		if n > 2 {
			label = q.subject[before : before+n]
		} else if !opener.bracketAfter {
			label = q.subject[opener.index:start]
		}
		if n == 0 {
			q.pos = save
		}
		if label != "" {
			if ref, ok := q.refs[normalizeLabel(label)]; ok {
				dest, title, matched = ref.dest, ref.title, true
			}
		}
	}
	if !matched {
		q.brackets = q.brackets[:len(q.brackets)-1]
		q.pos = start
		q.text(root, "]")
		return true
	}
	k := iLink
	if opener.image {
		k = iImage
	}
	link := q.newNode(k, "")
	q.nodes[link].dest, q.nodes[link].title = dest, title
	for t := q.nodes[opener.node].next; t >= 0; {
		next := q.nodes[t].next
		q.unlink(t)
		q.appendChild(link, t)
		t = next
	}
	q.appendChild(root, link)
	q.processEmphasis(opener.prevDelim)
	q.brackets = q.brackets[:len(q.brackets)-1]
	q.unlink(opener.node)
	if !opener.image {
		for i := range q.brackets {
			if !q.brackets[i].image {
				q.brackets[i].active = false
			}
		}
	}
	return true
}

// spnl moves past spaces and tabs, at most one line ending, and spaces
// and tabs.
func (q *inlineParser) spnl() {
	for q.peek() == ' ' || q.peek() == '\t' {
		q.pos++
	}
	if q.peek() == '\n' {
		q.pos++
		for q.peek() == ' ' || q.peek() == '\t' {
			q.pos++
		}
	}
}

// linkDestination reads a link destination: <...>, or a run with
// balanced parentheses and no space or control character.
func (q *inlineParser) linkDestination() (string, bool) {
	s := q.subject
	if q.peek() == '<' {
		i := q.pos + 1
		for i < len(s) {
			c := s[i]
			if c == '\\' && i+1 < len(s) && escapable(s[i+1]) {
				i += 2
				continue
			}
			if c == '>' {
				d := normalizeURI(unescapeString(s[q.pos+1 : i]))
				q.pos = i + 1
				return d, true
			}
			if c == '<' || c == '\n' {
				return "", false
			}
			i++
		}
		return "", false
	}
	start := q.pos
	open := 0
	for q.pos < len(s) {
		c := s[q.pos]
		if c == '\\' && q.pos+1 < len(s) && escapable(s[q.pos+1]) {
			q.pos += 2
		} else if c == '(' {
			q.pos++
			open++
		} else if c == ')' {
			if open < 1 {
				break
			}
			q.pos++
			open--
		} else if isSpace(c) {
			break
		} else {
			q.pos++
		}
	}
	if q.pos == start && q.peek() != ')' {
		return "", false
	}
	if open != 0 {
		q.pos = start
		return "", false
	}
	return normalizeURI(unescapeString(s[start:q.pos])), true
}

// linkTitle reads a link title in "", ” or ().
func (q *inlineParser) linkTitle() (string, bool) {
	s := q.subject
	if q.pos >= len(s) {
		return "", false
	}
	open := s[q.pos]
	close := open
	switch open {
	case '"', '\'':
	case '(':
		close = ')'
	default:
		return "", false
	}
	for i := q.pos + 1; i < len(s); i++ {
		c := s[i]
		if c == '\\' && i+1 < len(s) {
			i++
			continue
		}
		if c == close {
			t := unescapeString(s[q.pos+1 : i])
			q.pos = i + 1
			return t, true
		}
		if open == '(' && c == '(' {
			return "", false
		}
	}
	return "", false
}

// linkLabel is the length of the link label at the position, or 0: [ ]
// around at most 999 characters, with no unescaped bracket.
func (q *inlineParser) linkLabel() int {
	s := q.subject
	if q.peek() != '[' {
		return 0
	}
	start := q.pos
	i := start + 1
	n := 0
	for i < len(s) && n <= 1000 {
		c := s[i]
		if c == ']' {
			length := i + 1 - start
			if length > 1001 {
				return 0
			}
			q.pos = i + 1
			return length
		}
		if c == '[' {
			return 0
		}
		if c == '\\' && i+1 < len(s) {
			i += 2
		} else {
			i++
		}
		n++
	}
	return 0
}

// normalizeLabel is a link label's key: the text inside the brackets,
// trimmed, its whitespace runs one space, case folded.
func normalizeLabel(label string) string {
	s := trimSpace(label[1 : len(label)-1])
	var b strings.Builder
	space := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == ' ' || c == '\t' || c == '\r' || c == '\n' {
			space = true
			continue
		}
		if space {
			b.WriteByte(' ')
			space = false
		}
		b.WriteByte(c)
	}
	folded := strings.ToUpper(strings.ToLower(b.String()))
	return strings.ReplaceAll(folded, "ß", "SS")
}

func (q *inlineParser) autolink(root int) bool {
	s := q.subject[q.pos:]
	if n := emailAutolink(s); n > 0 {
		addr := s[1 : n-1]
		link := q.newNode(iLink, "")
		q.nodes[link].dest = normalizeURI("mailto:" + addr)
		q.appendChild(root, link)
		q.text(link, addr)
		q.pos += n
		return true
	}
	if n := uriAutolink(s); n > 0 {
		uri := s[1 : n-1]
		link := q.newNode(iLink, "")
		q.nodes[link].dest = normalizeURI(uri)
		q.appendChild(root, link)
		q.text(link, uri)
		q.pos += n
		return true
	}
	return false
}

func asciiLetter(c byte) bool { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }
func asciiDigit(c byte) bool  { return c >= '0' && c <= '9' }
func asciiAlnum(c byte) bool  { return asciiLetter(c) || asciiDigit(c) }

// uriAutolink is the length of <scheme:rest> at the start of s, or 0.
func uriAutolink(s string) int {
	if len(s) < 4 || s[0] != '<' || !asciiLetter(s[1]) {
		return 0
	}
	i := 2
	for i < len(s) && (asciiAlnum(s[i]) || s[i] == '.' || s[i] == '+' || s[i] == '-') {
		i++
	}
	if i-1 < 2 || i-1 > 32 || i >= len(s) || s[i] != ':' {
		return 0
	}
	for i++; i < len(s); i++ {
		c := s[i]
		if c == '>' {
			return i + 1
		}
		if c == '<' || c <= ' ' {
			return 0
		}
	}
	return 0
}

// emailAutolink is the length of <address> at the start of s, or 0.
func emailAutolink(s string) int {
	if len(s) < 3 || s[0] != '<' {
		return 0
	}
	i := 1
	for i < len(s) && (asciiAlnum(s[i]) || strings.IndexByte(".!#$%&'*+/=?^_`{|}~-", s[i]) >= 0) {
		i++
	}
	if i == 1 || i >= len(s) || s[i] != '@' {
		return 0
	}
	i++
	for {
		// a label: alnum, then up to 61 alnum or -, ending alnum
		if i >= len(s) || !asciiAlnum(s[i]) {
			return 0
		}
		j := i + 1
		for j < len(s) && (asciiAlnum(s[j]) || s[j] == '-') && j-i < 63 {
			j++
		}
		for j > i+1 && s[j-1] == '-' {
			j--
		}
		i = j
		if i < len(s) && s[i] == '.' {
			i++
			continue
		}
		break
	}
	if i < len(s) && s[i] == '>' {
		return i + 1
	}
	return 0
}

func (q *inlineParser) htmlTag(root int) bool {
	n := htmlTag(q.subject[q.pos:])
	if n == 0 {
		return false
	}
	q.appendChild(root, q.newNode(iHTML, q.subject[q.pos:q.pos+n]))
	q.pos += n
	return true
}

func (q *inlineParser) entity(root int) bool {
	n, text := entityAt(q.subject[q.pos:])
	if n == 0 {
		return false
	}
	q.text(root, text)
	q.pos += n
	return true
}

// entityAt reads an entity or numeric character reference at the start
// of s: its length and its text, or 0.
func entityAt(s string) (int, string) {
	if len(s) < 3 || s[0] != '&' {
		return 0, ""
	}
	if s[1] == '#' {
		i := 2
		hex := i < len(s) && (s[i] == 'x' || s[i] == 'X')
		if hex {
			i++
		}
		start := i
		for i < len(s) && (asciiDigit(s[i]) || (hex && strings.IndexByte("abcdefABCDEF", s[i]) >= 0)) {
			i++
		}
		n := i - start
		if n == 0 || (hex && n > 6) || (!hex && n > 7) || i >= len(s) || s[i] != ';' {
			return 0, ""
		}
		base := 10
		if hex {
			base = 16
		}
		v, _ := strconv.ParseInt(s[start:i], base, 64)
		r := rune(v)
		if v == 0 || v > 0x10FFFF || (v >= 0xD800 && v <= 0xDFFF) {
			r = 0xFFFD
		}
		return i + 1, string(r)
	}
	i := 1
	for i < len(s) && asciiAlnum(s[i]) && i <= 33 {
		i++
	}
	if i-1 < 2 || i-1 > 32 || i >= len(s) || s[i] != ';' || !asciiLetter(s[1]) {
		return 0, ""
	}
	text, ok := entityText(s[1:i])
	if !ok {
		return 0, ""
	}
	return i + 1, text
}

var entities map[string]string

// entityText is the text of the named reference name (without & and ;).
func entityText(name string) (string, bool) {
	if entities == nil {
		entities = map[string]string{}
		for _, entry := range strings.Split(entityTable, ",") {
			if entry == "" {
				continue
			}
			k, v, _ := strings.Cut(entry, ":")
			var b strings.Builder
			for _, h := range strings.Split(v, "+") {
				r, _ := strconv.ParseInt(h, 16, 32)
				b.WriteRune(rune(r))
			}
			entities[k] = b.String()
		}
	}
	t, ok := entities[name]
	return t, ok
}

// unescapeString is s with its backslash escapes and references read.
func unescapeString(s string) string {
	if strings.IndexByte(s, '\\') < 0 && strings.IndexByte(s, '&') < 0 {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		c := s[i]
		if c == '\\' && i+1 < len(s) && escapable(s[i+1]) {
			b.WriteByte(s[i+1])
			i += 2
			continue
		}
		if c == '&' {
			if n, text := entityAt(s[i:]); n > 0 {
				b.WriteString(text)
				i += n
				continue
			}
		}
		b.WriteByte(c)
		i++
	}
	return b.String()
}

// normalizeURI percent-encodes what a URL may not hold, keeping %XX.
func normalizeURI(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if asciiAlnum(c) || strings.IndexByte(";/?:@&=+$,-_.!~*'()#", c) >= 0 {
			b.WriteByte(c)
		} else if c == '%' && i+2 < len(s) && hexDigit(s[i+1]) && hexDigit(s[i+2]) {
			b.WriteString(s[i : i+3])
			i += 2
		} else {
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&15])
		}
	}
	return b.String()
}

func hexDigit(c byte) bool {
	return asciiDigit(c) || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

// parseReference reads link reference definitions at the start of s into
// refs, and is the length it read, or 0.
func parseReference(s string, refs map[string]linkRef) int {
	q := &inlineParser{subject: s, refs: refs}
	n := q.linkLabel()
	if n == 0 {
		return 0
	}
	raw := s[:n]
	if q.peek() != ':' {
		return 0
	}
	q.pos++
	q.spnl()
	dest, ok := q.linkDestination()
	if !ok {
		return 0
	}
	beforeTitle := q.pos
	q.spnl()
	title := ""
	hasTitle := false
	if q.pos != beforeTitle {
		title, hasTitle = q.linkTitle()
	}
	if !hasTitle {
		title = ""
		q.pos = beforeTitle
	}
	if !q.lineEnd() {
		if !hasTitle {
			return 0
		}
		title = ""
		q.pos = beforeTitle
		if !q.lineEnd() {
			return 0
		}
	}
	key := normalizeLabel(raw)
	if key == "" {
		return 0
	}
	if _, ok := refs[key]; !ok {
		refs[key] = linkRef{dest: dest, title: title}
	}
	return q.pos
}

// lineEnd moves past spaces and tabs and a line ending, or says there is
// more on the line.
func (q *inlineParser) lineEnd() bool {
	i := q.pos
	for i < len(q.subject) && (q.subject[i] == ' ' || q.subject[i] == '\t') {
		i++
	}
	if i < len(q.subject) && q.subject[i] != '\n' {
		return false
	}
	if i < len(q.subject) {
		i++
	}
	q.pos = i
	return true
}
