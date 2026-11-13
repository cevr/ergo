package derive

import (
	"fmt"
	"strconv"

	"ergo.local/prototype/internal/sketch/regexpplan"
)

// The byte pattern grammar (patterns.md, Pattern grammar). parsePattern
// reads printable ASCII text into regexpplan's closed AST; each node's
// Offset is the byte of the text that the node starts at (a repeat's is
// its operator), so a diagnostic maps back to the literal's source byte.

// PatternError is a refused pattern at byte Offset of its text.
type PatternError struct {
	Offset int
	Msg    string
}

func (e *PatternError) Error() string { return fmt.Sprintf("byte %d: %s", e.Offset, e.Msg) }

type pparser struct {
	text string
	i    int
	// names maps a capture name to the byte of its first use.
	names map[string]int
}

// parsed is a pattern's tree and, when its top level is an alternation
// written without a group, the branches of that alternation.
type parsed struct {
	root     *regexpplan.Node
	branches []*regexpplan.Node
	captures int
}

func parsePattern(text string) (pt *parsed, err *PatternError) {
	defer func() {
		if r := recover(); r != nil {
			e, ok := r.(*PatternError)
			if !ok {
				panic(r)
			}
			pt, err = nil, e
		}
	}()
	for i := 0; i < len(text); i++ {
		if c := text[i]; c < 0x20 || c > 0x7e {
			panic(&PatternError{i, fmt.Sprintf("a pattern is ASCII; write the byte as \\x%02X", c)})
		}
	}
	p := &pparser{text: text, names: map[string]int{}}
	root, branches := p.alternation()
	if p.i < len(text) {
		p.fail(p.i, "unmatched ): no group is open")
	}
	return &parsed{root: root, branches: branches, captures: len(p.names)}, nil
}

func (p *pparser) fail(at int, format string, args ...any) {
	panic(&PatternError{at, fmt.Sprintf(format, args...)})
}

func (p *pparser) peek() byte {
	if p.i < len(p.text) {
		return p.text[p.i]
	}
	return 0
}

func (p *pparser) has(s string) bool { return len(p.text)-p.i >= len(s) && p.text[p.i:p.i+len(s)] == s }

// alternation parses branches separated by |; it returns the branches too
// when there is more than one.
func (p *pparser) alternation() (*regexpplan.Node, []*regexpplan.Node) {
	start := p.i
	branches := []*regexpplan.Node{p.concat()}
	for p.peek() == '|' {
		p.i++
		branches = append(branches, p.concat())
	}
	if len(branches) == 1 {
		return branches[0], nil
	}
	return &regexpplan.Node{Kind: regexpplan.Alternative, Children: branches, Offset: start}, branches
}

func (p *pparser) concat() *regexpplan.Node {
	start := p.i
	var items []*regexpplan.Node
	for p.i < len(p.text) && p.peek() != '|' && p.peek() != ')' {
		items = append(items, p.repeat())
	}
	switch len(items) {
	case 0:
		return &regexpplan.Node{Kind: regexpplan.Empty, Offset: start}
	case 1:
		return items[0]
	}
	return &regexpplan.Node{Kind: regexpplan.Concat, Children: items, Offset: start}
}

func (p *pparser) repeat() *regexpplan.Node {
	n := p.atom()
	for {
		at := p.i
		min, max := 0, 0
		switch p.peek() {
		case '*':
			p.i++
			min, max = 0, -1
		case '+':
			p.i++
			min, max = 1, -1
		case '?':
			p.i++
			min, max = 0, 1
		case '{':
			min, max = p.bounds()
		default:
			return n
		}
		switch p.peek() {
		case '?':
			p.fail(p.i, "a lazy repeat is not a byte pattern form: write the greedy repeat; a find takes the leftmost match")
		case '+':
			p.fail(p.i, "a possessive repeat is not a byte pattern form: write the greedy repeat")
		}
		n = &regexpplan.Node{Kind: regexpplan.Repeat, Min: min, Max: max, Children: []*regexpplan.Node{n}, Offset: at}
	}
}

// bounds parses {n}, {n,} or {n,m}; any other brace is refused.
func (p *pparser) bounds() (int, int) {
	at := p.i
	p.i++
	number := func() (int, bool) {
		s := p.i
		for p.i < len(p.text) && p.peek() >= '0' && p.peek() <= '9' {
			p.i++
		}
		if s == p.i {
			return 0, false
		}
		v, err := strconv.Atoi(p.text[s:p.i])
		if err != nil || v > 1000 {
			p.fail(s, "a repeat bound is at most 1000")
		}
		return v, true
	}
	min, ok := number()
	if !ok {
		p.fail(at, "a brace starts a repeat {n}, {n,} or {n,m}: write \\{ for the byte")
	}
	max := min
	if p.peek() == ',' {
		p.i++
		if v, ok := number(); ok {
			max = v
		} else {
			max = -1
		}
	}
	if p.peek() != '}' {
		p.fail(at, "a brace starts a repeat {n}, {n,} or {n,m}: write \\{ for the byte")
	}
	p.i++
	if max >= 0 && max < min {
		p.fail(at, "the repeat {%d,%d} has its bounds reversed", min, max)
	}
	return min, max
}

func (p *pparser) atom() *regexpplan.Node {
	at := p.i
	switch c := p.peek(); c {
	case '(':
		return p.group()
	case '[':
		return p.class()
	case '.':
		p.i++
		return setNode(at, func(b int) bool { return b != '\n' })
	case '^', '$':
		p.fail(at, "a parse matches the whole text and a find the leftmost match; drop %c", c)
	case '*', '+', '?':
		p.fail(at, "this repeat has nothing to repeat")
	case '{':
		p.fail(at, "a brace starts a repeat {n}, {n,} or {n,m}: write \\{ for the byte")
	case '\\':
		set := p.escape(false)
		n := &regexpplan.Node{Kind: regexpplan.Bytes, Set: set, Offset: at}
		return n
	}
	p.i++
	return byteNode(at, p.text[at])
}

func byteNode(at int, b byte) *regexpplan.Node {
	n := regexpplan.Byte(b)
	n.Offset = at
	return n
}

func setNode(at int, in func(int) bool) *regexpplan.Node {
	n := &regexpplan.Node{Kind: regexpplan.Bytes, Offset: at}
	for b := 0; b < 256; b++ {
		if in(b) {
			n.Set[b/64] |= 1 << (b % 64)
		}
	}
	return n
}

func (p *pparser) group() *regexpplan.Node {
	at := p.i
	p.i++
	name := ""
	switch {
	case p.has("?<=") || p.has("?<!"), p.has("?=") || p.has("?!"):
		p.fail(at, "a lookaround is not a byte pattern form: match the context and capture the part you need")
	case p.has("?P<"):
		p.fail(at, "write (?<Name>…): the P form is not used")
	case p.has("?<"):
		p.i += 2
		s := p.i
		for p.i < len(p.text) && p.peek() != '>' {
			p.i++
		}
		if p.i == len(p.text) {
			p.fail(at, "unclosed capture name: write (?<Name>…)")
		}
		name = p.text[s:p.i]
		p.i++
		if !isIdent(name) {
			p.fail(s, "the capture name %q is not an identifier", name)
		}
		if first, ok := p.names[name]; ok {
			p.fail(s, "the capture %s is named twice; the first is at byte %d", name, first)
		}
		p.names[name] = s
	case p.has("?:"):
		p.fail(at, "write ( … ): a group captures only with a name")
	case p.has("?"):
		p.fail(at, "a flag group is not a byte pattern form: write the bytes, as [Aa] for either case")
	}
	inner, _ := p.alternation()
	if p.peek() != ')' {
		p.fail(at, "unclosed group")
	}
	p.i++
	if name == "" {
		return inner
	}
	return &regexpplan.Node{Kind: regexpplan.Capture, Name: name, Children: []*regexpplan.Node{inner}, Offset: at}
}

func isIdent(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		letter := c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
		if !letter && (i == 0 || c < '0' || c > '9') {
			return false
		}
	}
	return true
}

type byteSet = [4]uint64

func setOf(in func(int) bool) byteSet {
	var s byteSet
	for b := 0; b < 256; b++ {
		if in(b) {
			s[b/64] |= 1 << (b % 64)
		}
	}
	return s
}

var (
	digit = setOf(func(b int) bool { return b >= '0' && b <= '9' })
	word  = setOf(func(b int) bool {
		return b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b == '_'
	})
	space = setOf(func(b int) bool { return b == '\t' || b == '\n' || b == '\f' || b == '\r' || b == ' ' })
)

func complement(s byteSet) byteSet { return byteSet{^s[0], ^s[1], ^s[2], ^s[3]} }

const metachars = `.\()[]{}|*+?^$-`

// escape reads one escape at p.i and returns its byte set. In a class,
// the multi-byte classes \d \w \s and their complements are admitted too.
func (p *pparser) escape(inClass bool) byteSet {
	at := p.i
	p.i++
	if p.i >= len(p.text) {
		p.fail(at, "a pattern ends inside an escape: write \\\\ for the byte \\")
	}
	c := p.text[p.i]
	p.i++
	one := func(b byte) byteSet { return setOf(func(v int) bool { return v == int(b) }) }
	switch c {
	case 'd':
		return digit
	case 'w':
		return word
	case 's':
		return space
	case 'D':
		return complement(digit)
	case 'W':
		return complement(word)
	case 'S':
		return complement(space)
	case 'n':
		return one('\n')
	case 'r':
		return one('\r')
	case 't':
		return one('\t')
	case 'f':
		return one('\f')
	case 'v':
		return one('\v')
	case 'x':
		if p.i+2 > len(p.text) {
			p.fail(at, "write \\x with two hexadecimal digits")
		}
		v, err := strconv.ParseUint(p.text[p.i:p.i+2], 16, 8)
		if err != nil {
			p.fail(at, "write \\x with two hexadecimal digits")
		}
		p.i += 2
		return one(byte(v))
	case 'A', 'z', 'b', 'B':
		p.fail(at, "a parse matches the whole text and a find the leftmost match; drop \\%c", c)
	case 'p', 'P':
		p.fail(at, "a Unicode class is not a byte pattern form: write a byte class, as [A-Za-z]")
	case 'k':
		p.fail(at, "a backreference is not a byte pattern form: capture both parts and compare them")
	}
	if c >= '1' && c <= '9' {
		p.fail(at, "a backreference is not a byte pattern form: capture both parts and compare them")
	}
	for i := 0; i < len(metachars); i++ {
		if metachars[i] == c {
			return one(c)
		}
	}
	p.fail(at, "\\%c is not an escape of a byte pattern", c)
	return byteSet{}
}

// class parses [...] or [^...].
func (p *pparser) class() *regexpplan.Node {
	at := p.i
	p.i++
	if p.has("[:") {
		p.fail(p.i, "a POSIX class is not a byte pattern form: write the bytes, as [A-Za-z]")
	}
	negate := false
	if p.peek() == '^' {
		negate = true
		p.i++
	}
	var set byteSet
	first := true
	for {
		if p.i >= len(p.text) {
			p.fail(at, "unclosed class: write ] to end it")
		}
		if p.peek() == ']' && !first {
			p.i++
			break
		}
		first = false
		lo, many, ok := p.classItem()
		if !ok {
			for k := range set {
				set[k] |= many[k]
			}
			continue
		}
		if p.peek() == '-' && p.i+1 < len(p.text) && p.text[p.i+1] != ']' {
			dash := p.i
			p.i++
			hi, _, single := p.classItem()
			if !single {
				p.fail(dash, "a range ends at one byte, not a class")
			}
			if hi < lo {
				p.fail(dash, "the range %c-%c has its low end above its high end", lo, hi)
			}
			for b := int(lo); b <= int(hi); b++ {
				set[b/64] |= 1 << (b % 64)
			}
			continue
		}
		set[lo/64] |= 1 << (lo % 64)
	}
	if negate {
		set = complement(set)
	}
	if set == (byteSet{}) {
		p.fail(at, "this class holds no byte")
	}
	return &regexpplan.Node{Kind: regexpplan.Bytes, Set: set, Offset: at}
}

// classItem reads one class item: a single byte (and true), or the set of
// a multi-byte escape such as \d (and false).
func (p *pparser) classItem() (byte, byteSet, bool) {
	if p.peek() != '\\' {
		b := p.text[p.i]
		if b == '[' && p.has("[:") {
			p.fail(p.i, "a POSIX class is not a byte pattern form: write the bytes, as [A-Za-z]")
		}
		p.i++
		return b, byteSet{}, true
	}
	set := p.escape(true)
	if b, ok := single(set); ok {
		return b, set, true
	}
	return 0, set, false
}

func single(s byteSet) (byte, bool) {
	found := -1
	for b := 0; b < 256; b++ {
		if s[b/64]&(1<<(b%64)) != 0 {
			if found >= 0 {
				return 0, false
			}
			found = b
		}
	}
	return byte(found), found >= 0
}
