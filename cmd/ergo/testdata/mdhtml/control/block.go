package main

import "strings"

// The block phase: lines into a tree of blocks, held in one slice and
// linked by ids, as CommonMark 0.31.2 describes and commonmark.js does
// it: each line first continues the open blocks it can, then starts new
// ones, then adds its text to the deepest open block.

type blockKind uint8

const (
	bDocument blockKind = iota
	bQuote
	bList
	bItem
	bPara
	bHeading
	bRule
	bCode
	bHTML
)

// listData is a list's or an item's marker.
type listData struct {
	ordered      bool
	bullet       byte
	start        int
	delim        byte
	padding      int
	markerOffset int
	tight        bool
}

type block struct {
	kind                  blockKind
	parent, first, last   int
	prev, next            int
	open                  bool
	startLine, endLine    int
	list                  listData
	level                 int
	fenced                bool
	fenceChar             byte
	fenceLen, fenceOffset int
	htmlType              int
	lines                 []string // the text lines, each without its line ending
	content               string   // a paragraph's or heading's raw text, once closed
	info, literal         string
	inlines               int // the inline root of a paragraph or heading
}

type linkRef struct{ dest, title string }

type parser struct {
	blocks               []block
	tip, oldtip          int
	lastMatched          int
	line                 string
	lineNumber           int
	offset, column       int
	nextNonspace         int
	nextNonspaceColumn   int
	indent               int
	indented, blank      bool
	partiallyConsumedTab bool
	allClosed            bool
	refs                 map[string]linkRef
}

func (p *parser) add(k blockKind, line int) int {
	p.blocks = append(p.blocks, block{kind: k, parent: -1, first: -1, last: -1, prev: -1, next: -1, open: true, startLine: line, inlines: -1})
	return len(p.blocks) - 1
}

func (p *parser) appendChild(parent, child int) {
	b := &p.blocks[child]
	b.parent = parent
	b.prev = p.blocks[parent].last
	if b.prev >= 0 {
		p.blocks[b.prev].next = child
	} else {
		p.blocks[parent].first = child
	}
	p.blocks[parent].last = child
}

func (p *parser) unlink(id int) {
	b := p.blocks[id]
	if b.prev >= 0 {
		p.blocks[b.prev].next = b.next
	} else if b.parent >= 0 {
		p.blocks[b.parent].first = b.next
	}
	if b.next >= 0 {
		p.blocks[b.next].prev = b.prev
	} else if b.parent >= 0 {
		p.blocks[b.parent].last = b.prev
	}
	p.blocks[id].parent, p.blocks[id].prev, p.blocks[id].next = -1, -1, -1
}

// parseBlocks reads the document src into blocks; block 0 is the document.
func parseBlocks(src string) *parser {
	p := &parser{refs: map[string]linkRef{}}
	p.add(bDocument, 1)
	lines := splitLines(src)
	for _, ln := range lines {
		p.incorporate(ln)
	}
	for p.tip >= 0 {
		p.finalize(p.tip, len(lines))
	}
	return p
}

// splitLines is src's lines without their endings (\n, \r\n or \r); a
// line ending at the end makes no last empty line. NUL becomes U+FFFD.
func splitLines(src string) []string {
	if strings.IndexByte(src, 0) >= 0 {
		src = strings.ReplaceAll(src, "\x00", "\uFFFD")
	}
	var lines []string
	start := 0
	for i := 0; i < len(src); i++ {
		c := src[i]
		if c == '\n' || c == '\r' {
			lines = append(lines, src[start:i])
			if c == '\r' && i+1 < len(src) && src[i+1] == '\n' {
				i++
			}
			start = i + 1
		}
	}
	if start < len(src) {
		lines = append(lines, src[start:])
	}
	return lines
}

func (p *parser) findNextNonspace() {
	i, cols := p.offset, p.column
	for i < len(p.line) {
		c := p.line[i]
		if c == ' ' {
			i++
			cols++
		} else if c == '\t' {
			i++
			cols += 4 - cols%4
		} else {
			break
		}
	}
	p.blank = i >= len(p.line)
	p.nextNonspace, p.nextNonspaceColumn = i, cols
	p.indent = cols - p.column
	p.indented = p.indent >= 4
}

func (p *parser) advanceNextNonspace() {
	p.offset, p.column = p.nextNonspace, p.nextNonspaceColumn
	p.partiallyConsumedTab = false
}

// advanceOffset moves count bytes, or count columns when columns is set
// (a tab may then be consumed in part).
func (p *parser) advanceOffset(count int, columns bool) {
	for count > 0 && p.offset < len(p.line) {
		if p.line[p.offset] == '\t' {
			toTab := 4 - p.column%4
			if columns {
				p.partiallyConsumedTab = toTab > count
				n := min(toTab, count)
				p.column += n
				if !p.partiallyConsumedTab {
					p.offset++
				}
				count -= n
			} else {
				p.partiallyConsumedTab = false
				p.column += toTab
				p.offset++
				count--
			}
		} else {
			p.partiallyConsumedTab = false
			p.offset++
			p.column++
			count--
		}
	}
}

func (p *parser) at(i int) int {
	if i < len(p.line) {
		return int(p.line[i])
	}
	return -1
}

func spaceOrTab(c int) bool { return c == ' ' || c == '\t' }

// addLine adds the rest of the line to the tip.
func (p *parser) addLine() {
	rest := ""
	if p.partiallyConsumedTab {
		p.offset++
		rest = strings.Repeat(" ", 4-p.column%4)
	}
	if p.offset < len(p.line) {
		rest += p.line[p.offset:]
	}
	p.blocks[p.tip].lines = append(p.blocks[p.tip].lines, rest)
}

func canContain(k, child blockKind) bool {
	switch k {
	case bDocument, bQuote, bItem:
		return child != bItem
	case bList:
		return child == bItem
	}
	return false
}

func acceptsLines(k blockKind) bool { return k == bPara || k == bCode || k == bHTML }

// addChild adds a block of kind k at the tip, closing the tips that
// cannot hold it.
func (p *parser) addChild(k blockKind) int {
	for !canContain(p.blocks[p.tip].kind, k) {
		p.finalize(p.tip, p.lineNumber-1)
	}
	id := p.add(k, p.lineNumber)
	p.appendChild(p.tip, id)
	p.tip = id
	return id
}

func (p *parser) closeUnmatched() {
	if !p.allClosed {
		for p.oldtip != p.lastMatched {
			parent := p.blocks[p.oldtip].parent
			p.finalize(p.oldtip, p.lineNumber-1)
			p.oldtip = parent
		}
		p.allClosed = true
	}
}

// continues says whether the open block id goes on on this line: 0 it
// does, 1 it does not, 2 the line is used up (a closing fence).
func (p *parser) continues(id int) int {
	b := &p.blocks[id]
	switch b.kind {
	case bDocument, bList:
		return 0
	case bQuote:
		if !p.indented && p.at(p.nextNonspace) == '>' {
			p.advanceNextNonspace()
			p.advanceOffset(1, false)
			if spaceOrTab(p.at(p.offset)) {
				p.advanceOffset(1, true)
			}
			return 0
		}
		return 1
	case bItem:
		if p.blank {
			if b.first < 0 {
				return 1
			}
			p.advanceNextNonspace()
			return 0
		}
		if p.indent >= b.list.markerOffset+b.list.padding {
			p.advanceOffset(b.list.markerOffset+b.list.padding, true)
			return 0
		}
		return 1
	case bHeading, bRule:
		return 1
	case bCode:
		if b.fenced {
			if p.indent <= 3 && p.at(p.nextNonspace) == int(b.fenceChar) {
				n := closingFence(p.line[p.nextNonspace:], b.fenceChar)
				if n >= b.fenceLen {
					p.finalize(id, p.lineNumber)
					return 2
				}
			}
			for i := b.fenceOffset; i > 0 && spaceOrTab(p.at(p.offset)); i-- {
				p.advanceOffset(1, true)
			}
			return 0
		}
		if p.indent >= 4 {
			p.advanceOffset(4, true)
			return 0
		}
		if p.blank {
			p.advanceNextNonspace()
			return 0
		}
		return 1
	case bHTML:
		if p.blank && (b.htmlType == 6 || b.htmlType == 7) {
			return 1
		}
		return 0
	case bPara:
		if p.blank {
			return 1
		}
		return 0
	}
	return 1
}

// closingFence is the length of the run of c that s starts with when
// only spaces and tabs follow it and it is at least three long, else 0.
func closingFence(s string, c byte) int {
	n := 0
	for n < len(s) && s[n] == c {
		n++
	}
	if n < 3 {
		return 0
	}
	for i := n; i < len(s); i++ {
		if s[i] != ' ' && s[i] != '\t' {
			return 0
		}
	}
	return n
}

func (p *parser) incorporate(ln string) {
	container := 0
	p.oldtip = p.tip
	p.offset, p.column = 0, 0
	p.blank, p.partiallyConsumedTab = false, false
	p.lineNumber++
	p.line = ln
	allMatched := true
	for {
		last := p.blocks[container].last
		if last < 0 || !p.blocks[last].open {
			break
		}
		container = last
		p.findNextNonspace()
		switch p.continues(container) {
		case 1:
			container = p.blocks[container].parent
			allMatched = false
		case 2:
			return
		}
		if !allMatched {
			break
		}
	}
	p.allClosed = container == p.oldtip
	p.lastMatched = container
	k := p.blocks[container].kind
	matchedLeaf := k != bPara && acceptsLines(k)
	for !matchedLeaf {
		p.findNextNonspace()
		if !p.indented && !maybeSpecial(p.at(p.nextNonspace)) {
			p.advanceNextNonspace()
			break
		}
		res := 0
		for s := 0; s < 8 && res == 0; s++ {
			res = p.start(s, container)
		}
		if res == 0 {
			p.advanceNextNonspace()
			break
		}
		container = p.tip
		if res == 2 {
			matchedLeaf = true
		}
	}
	if !p.allClosed && !p.blank && p.blocks[p.tip].kind == bPara {
		p.addLine() // a lazy continuation line
		return
	}
	p.closeUnmatched()
	k = p.blocks[container].kind
	if acceptsLines(k) {
		p.addLine()
		if k == bHTML && p.blocks[container].htmlType <= 5 && htmlBlockEnds(p.blocks[container].htmlType, p.line[min(p.offset, len(p.line)):]) {
			p.finalize(container, p.lineNumber)
		}
	} else if p.offset < len(ln) && !p.blank {
		p.addChild(bPara)
		p.advanceNextNonspace()
		p.addLine()
	}
}

func maybeSpecial(c int) bool {
	switch c {
	case '#', '`', '~', '*', '+', '_', '=', '<', '>', '-':
		return true
	}
	return c >= '0' && c <= '9'
}

// start tries block start s (0 to 7, in CommonMark's order) at the line:
// 0 no start, 1 a container started, 2 a leaf started.
func (p *parser) start(s int, container int) int {
	rest := p.line[p.nextNonspace:]
	switch s {
	case 0: // block quote
		if !p.indented && p.at(p.nextNonspace) == '>' {
			p.advanceNextNonspace()
			p.advanceOffset(1, false)
			if spaceOrTab(p.at(p.offset)) {
				p.advanceOffset(1, true)
			}
			p.closeUnmatched()
			p.addChild(bQuote)
			return 1
		}
	case 1: // ATX heading
		if n := atxMarker(rest); !p.indented && n > 0 {
			p.advanceNextNonspace()
			level := 0
			for level < len(rest) && rest[level] == '#' {
				level++
			}
			p.advanceOffset(n, false)
			p.closeUnmatched()
			id := p.addChild(bHeading)
			p.blocks[id].level = level
			p.blocks[id].content = atxContent(p.line[p.offset:])
			p.advanceOffset(len(p.line)-p.offset, false)
			return 2
		}
	case 2: // fenced code
		if n := openingFence(rest); !p.indented && n > 0 {
			p.closeUnmatched()
			id := p.addChild(bCode)
			b := &p.blocks[id]
			b.fenced, b.fenceLen, b.fenceChar, b.fenceOffset = true, n, rest[0], p.indent
			p.advanceNextNonspace()
			p.advanceOffset(n, false)
			return 2
		}
	case 3: // HTML block
		if !p.indented && p.at(p.nextNonspace) == '<' {
			for t := 1; t <= 7; t++ {
				if htmlBlockStarts(t, rest) && (t < 7 || (p.blocks[container].kind != bPara && !(!p.allClosed && !p.blank && p.blocks[p.tip].kind == bPara))) {
					p.closeUnmatched()
					id := p.addChild(bHTML)
					p.blocks[id].htmlType = t
					return 2
				}
			}
		}
	case 4: // setext heading underline
		if level := setextLevel(rest); !p.indented && p.blocks[container].kind == bPara && level > 0 {
			p.closeUnmatched()
			content := p.text(container)
			for len(content) > 0 && content[0] == '[' {
				n := parseReference(content, p.refs)
				if n == 0 {
					break
				}
				content = content[n:]
			}
			if content != "" {
				b := &p.blocks[container]
				b.kind, b.level, b.content, b.lines = bHeading, level, content, nil
				p.tip = container
				p.advanceOffset(len(p.line)-p.offset, false)
				return 2
			}
			// Only definitions: the paragraph reads them when it closes.
		}
	case 5: // thematic break
		if !p.indented && thematicBreak(rest) {
			p.closeUnmatched()
			p.addChild(bRule)
			p.advanceOffset(len(p.line)-p.offset, false)
			return 2
		}
	case 6: // list item
		if !p.indented || p.blocks[container].kind == bList {
			if data, ok := p.listMarker(container); ok {
				p.closeUnmatched()
				t := p.blocks[p.tip]
				if t.kind != bList || !listsMatch(t.list, data) {
					id := p.addChild(bList)
					p.blocks[id].list = data
				}
				id := p.addChild(bItem)
				p.blocks[id].list = data
				return 1
			}
		}
	case 7: // indented code
		if p.indented && p.blocks[p.tip].kind != bPara && !p.blank {
			p.advanceOffset(4, true)
			p.closeUnmatched()
			p.addChild(bCode)
			return 2
		}
	}
	return 0
}

// atxMarker is the length of an ATX heading's opening (one to six #s and
// a space or tab, or the end), or 0.
func atxMarker(s string) int {
	n := 0
	for n < len(s) && s[n] == '#' {
		n++
	}
	if n == 0 || n > 6 {
		return 0
	}
	if n == len(s) {
		return n
	}
	if s[n] != ' ' && s[n] != '\t' {
		return 0
	}
	for n < len(s) && (s[n] == ' ' || s[n] == '\t') {
		n++
	}
	return n
}

// atxContent is a heading's text without its closing sequence.
func atxContent(s string) string {
	// ^[ \t]*#+[ \t]*$ is all closing sequence
	i := 0
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	j := i
	for j < len(s) && s[j] == '#' {
		j++
	}
	if j > i {
		k := j
		for k < len(s) && (s[k] == ' ' || s[k] == '\t') {
			k++
		}
		if k == len(s) {
			return ""
		}
	}
	// [ \t]+#+[ \t]*$
	e := len(s)
	for e > 0 && (s[e-1] == ' ' || s[e-1] == '\t') {
		e--
	}
	h := e
	for h > 0 && s[h-1] == '#' {
		h--
	}
	if h < e && h > 0 && (s[h-1] == ' ' || s[h-1] == '\t') {
		for h > 0 && (s[h-1] == ' ' || s[h-1] == '\t') {
			h--
		}
		return s[:h]
	}
	return s
}

// openingFence is the length of a code fence that s starts with, or 0:
// three or more backticks with no backtick after them, or tildes.
func openingFence(s string) int {
	if s == "" || (s[0] != '`' && s[0] != '~') {
		return 0
	}
	c := s[0]
	n := 0
	for n < len(s) && s[n] == c {
		n++
	}
	if n < 3 {
		return 0
	}
	if c == '`' && strings.IndexByte(s[n:], '`') >= 0 {
		return 0
	}
	return n
}

// setextLevel is 1 for a line of =s, 2 for one of -s (with spaces or
// tabs after), else 0.
func setextLevel(s string) int {
	if s == "" || (s[0] != '=' && s[0] != '-') {
		return 0
	}
	c := s[0]
	i := 0
	for i < len(s) && s[i] == c {
		i++
	}
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	if i != len(s) {
		return 0
	}
	if c == '=' {
		return 1
	}
	return 2
}

// thematicBreak says whether s is three or more *, - or _ (one of them)
// with spaces or tabs between.
func thematicBreak(s string) bool {
	if s == "" || (s[0] != '*' && s[0] != '-' && s[0] != '_') {
		return false
	}
	c := s[0]
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			n++
		} else if s[i] != ' ' && s[i] != '\t' {
			return false
		}
	}
	return n >= 3
}

func listsMatch(a, b listData) bool {
	return a.ordered == b.ordered && a.delim == b.delim && a.bullet == b.bullet
}

// listMarker reads a list item's marker at the line, and moves past it
// and the spaces of its content's indent.
func (p *parser) listMarker(container int) (listData, bool) {
	if p.indent >= 4 {
		return listData{}, false
	}
	rest := p.line[p.nextNonspace:]
	data := listData{tight: true, markerOffset: p.indent}
	n := 0
	if rest != "" && (rest[0] == '*' || rest[0] == '+' || rest[0] == '-') {
		data.bullet = rest[0]
		n = 1
	} else {
		d := 0
		for d < len(rest) && d < 10 && rest[d] >= '0' && rest[d] <= '9' {
			d++
		}
		if d == 0 || d > 9 || d >= len(rest) || (rest[d] != '.' && rest[d] != ')') {
			return listData{}, false
		}
		start := 0
		for i := 0; i < d; i++ {
			start = start*10 + int(rest[i]-'0')
		}
		if p.blocks[container].kind == bPara && start != 1 {
			return listData{}, false
		}
		data.ordered, data.start, data.delim = true, start, rest[d]
		n = d + 1
	}
	next := p.at(p.nextNonspace + n)
	if next != -1 && next != '\t' && next != ' ' {
		return listData{}, false
	}
	if p.blocks[container].kind == bPara && isBlank(p.line[p.nextNonspace+n:]) {
		return listData{}, false
	}
	p.advanceNextNonspace()
	p.advanceOffset(n, true)
	startCol, startOffset := p.column, p.offset
	for {
		p.advanceOffset(1, true)
		if !(p.column-startCol < 5 && spaceOrTab(p.at(p.offset))) {
			break
		}
	}
	blankItem := p.at(p.offset) == -1
	spaces := p.column - startCol
	if spaces >= 5 || spaces < 1 || blankItem {
		data.padding = n + 1
		p.column, p.offset = startCol, startOffset
		if spaceOrTab(p.at(p.offset)) {
			p.advanceOffset(1, true)
		}
	} else {
		data.padding = n + spaces
	}
	return data, true
}

// blankFrom says whether s holds only spaces and tabs from i on.
func blankFrom(s string, i int) bool {
	for ; i < len(s); i++ {
		if s[i] != ' ' && s[i] != '\t' {
			return false
		}
	}
	return true
}

// text is the block's lines, each ended by a line feed.
func (p *parser) text(id int) string {
	b := p.blocks[id]
	if len(b.lines) == 0 {
		return ""
	}
	return strings.Join(b.lines, "\n") + "\n"
}

func (p *parser) finalize(id int, line int) {
	b := &p.blocks[id]
	above := b.parent
	b.open = false
	b.endLine = line
	switch b.kind {
	case bPara:
		content := p.text(id)
		refs := false
		for len(content) > 0 && content[0] == '[' {
			n := parseReference(content, p.refs)
			if n == 0 {
				break
			}
			content = content[n:]
			refs = true
		}
		p.blocks[id].content, p.blocks[id].lines = content, nil
		if refs && isBlank(content) {
			p.unlink(id)
		}
	case bCode:
		if b.fenced {
			first := ""
			if len(b.lines) > 0 {
				first = b.lines[0]
			}
			b.info = unescapeString(trimSpace(first))
			if len(b.lines) > 1 {
				b.literal = strings.Join(b.lines[1:], "\n") + "\n"
			}
		} else {
			n := len(b.lines)
			for n > 0 && blankFrom(b.lines[n-1], 0) {
				n--
			}
			b.literal = strings.Join(b.lines[:n], "\n") + "\n"
		}
		b.lines = nil
	case bHTML:
		s := p.text(id)
		// drop the trailing run of (\n *)+
		e := len(s)
		for {
			i := e
			for i > 0 && s[i-1] == ' ' {
				i--
			}
			if i > 0 && s[i-1] == '\n' {
				e = i - 1
				continue
			}
			break
		}
		b.literal, b.lines = s[:e], nil
	case bList:
		b.list.tight = true
		for item := b.first; item >= 0; item = p.blocks[item].next {
			if p.blocks[item].next >= 0 && p.endsBlank(item) {
				b.list.tight = false
				break
			}
			for sub := p.blocks[item].first; sub >= 0; sub = p.blocks[sub].next {
				if p.blocks[sub].next >= 0 && p.endsBlank(sub) {
					b.list.tight = false
					break
				}
			}
			if !b.list.tight {
				break
			}
		}
		b.endLine = p.blocks[b.last].endLine
	case bItem:
		if b.last >= 0 {
			b.endLine = p.blocks[b.last].endLine
		} else {
			b.endLine = b.startLine
		}
	}
	p.tip = above
}

// endsBlank says whether a blank line follows the block before its next
// sibling.
func (p *parser) endsBlank(id int) bool {
	b := p.blocks[id]
	return b.next >= 0 && b.endLine != p.blocks[b.next].startLine-1
}

func isBlank(s string) bool {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case ' ', '\t', '\n', '\r', '\f', '\v':
		default:
			return false
		}
	}
	return true
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v'
}

// trimSpace is s without the ASCII whitespace at its ends.
func trimSpace(s string) string {
	i, j := 0, len(s)
	for i < j && isSpace(s[i]) {
		i++
	}
	for j > i && isSpace(s[j-1]) {
		j--
	}
	return s[i:j]
}
