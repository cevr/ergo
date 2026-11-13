package check

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Patterns (types.md, Refinements 11). `type Email string where
// matches(it, "[^@ ]+@[^@ ]+")` refines a string by a pattern that the
// checker compiles here, once, to a deterministic automaton over runes: a
// pattern matches the whole value, as Go's regexp does `^(?:p)$`. The
// lowering writes the automaton as a straight-line Go function of the
// value (lower, pattern.go), so no regexp is built when the program runs;
// the generator walks it to draw a value of a chosen length that matches.
//
// The subset is the regular part of Go's syntax (RE2): literal runes, the
// escapes of punctuation, \t \n \r \f \v, \d \D \w \W \s \S, `.` (any rune
// but a newline), classes `[…]` and `[^…]` of runes, ranges and those
// escapes, groups `(…)` and `(?:…)`, alternation, and the repeats `*`,
// `+`, `?`, `{n}`, `{n,}`, `{n,m}` (n, m ≤ 100), each optionally lazy
// (`*?`), which a whole-value match does not tell apart. `^` first and `$`
// last say what the whole match already does. Anything else is refused at
// its offset in the pattern: flags (?i), named groups, \b and the other
// assertions, \p classes, \x and octal escapes, POSIX classes, a `{` that
// starts no repeat, `^` or `$` elsewhere, and a class of surrogates only.
//
// Prior art: Go's regexp/syntax parses the same syntax and compiles it to
// an NFA run at match time; RE2 and Rust's regex build the DFA lazily at
// run time; re2c and Ragel compile a pattern to a straight-line matcher
// at build time, the form this lowering writes; Rust's regex-automata can
// serialise a DFA built ahead of time.

// Matcher is a compiled pattern: a minimal deterministic automaton over
// runes. State 0 is the start. A rune with no edge from a state rejects:
// the dead state is left out.
type Matcher struct {
	// Source is the pattern as the where writes it, decoded.
	Source string
	States []PatternState
}

// PatternState is one state of a Matcher.
type PatternState struct {
	Accept bool
	// Edges are sorted by Lo, disjoint and not adjacent to an edge of the
	// same target.
	Edges []PatternEdge
}

// PatternEdge takes a rune Lo..Hi to the state To.
type PatternEdge struct {
	Lo, Hi rune
	To     int
}

// PatternError is a pattern outside the subset, at the byte Offset of the
// decoded pattern.
type PatternError struct {
	Offset int
	Msg    string
}

func (e *PatternError) Error() string { return e.Msg }

// patternMaxRepeat bounds the counts of a repeat, and patternMaxStates the
// automaton, so a pattern's matcher stays small.
const (
	patternMaxRepeat = 100
	patternMaxStates = 2000
	patternMaxNFA    = 20000
)

// Match reports whether the pattern matches the whole of s. An invalid
// UTF-8 byte reads as U+FFFD, as Go's range and regexp read it.
func (p *Matcher) Match(s string) bool {
	q := 0
	for _, r := range s {
		next := -1
		for _, e := range p.States[q].Edges {
			if r < e.Lo {
				break
			}
			if r <= e.Hi {
				next = e.To
				break
			}
		}
		if next < 0 {
			return false
		}
		q = next
	}
	return p.States[q].Accept
}

// Reach is, for each byte length k up to max, the states from which the
// pattern accepts after exactly k more bytes of valid UTF-8:
// Reach(max)[k][q]. A refinement's len(it) counts bytes, so the generator
// draws a length in bytes and walks runes of the widths that fit it.
func (p *Matcher) Reach(max int) [][]bool {
	out := make([][]bool, max+1)
	out[0] = make([]bool, len(p.States))
	for q, s := range p.States {
		out[0][q] = s.Accept
	}
	for k := 1; k <= max; k++ {
		out[k] = make([]bool, len(p.States))
		for q, s := range p.States {
			for _, e := range s.Edges {
				for w := 1; w <= 4 && w <= k && !out[k][q]; w++ {
					out[k][q] = out[k-w][e.To] && len(WidthSpans(e.Lo, e.Hi, w)) > 0
				}
			}
		}
	}
	return out
}

// Generate is a value of n bytes that the pattern matches, drawn by
// pick(m), a choice in 0..m-1, or false when no value of n bytes matches.
// Each step takes an edge and a UTF-8 width that can still reach an
// accepting state in the bytes left, each pair as likely, then a rune of
// that width on the edge (PickRune). The lowering's harness writes the
// same walk in Go (lower, pattern.go).
func (p *Matcher) Generate(n int, pick func(int) int) (string, bool) {
	reach := p.Reach(n)
	if !reach[n][0] {
		return "", false
	}
	var b strings.Builder
	q := 0
	for left := n; left > 0; {
		type option struct {
			e PatternEdge
			w int
		}
		var ok []option
		for _, e := range p.States[q].Edges {
			for w := 1; w <= 4 && w <= left; w++ {
				if reach[left-w][e.To] && len(WidthSpans(e.Lo, e.Hi, w)) > 0 {
					ok = append(ok, option{e, w})
				}
			}
		}
		o := ok[pick(len(ok))]
		b.WriteRune(PickRune(WidthSpans(o.e.Lo, o.e.Hi, o.w), pick))
		q, left = o.e.To, left-o.w
	}
	return b.String(), true
}

// utf8Bands are the runes of each UTF-8 width 1 to 4, without the
// surrogates, which no string holds.
var utf8Bands = [5][][2]rune{
	nil,
	{{0, 0x7F}},
	{{0x80, 0x7FF}},
	{{0x800, 0xD7FF}, {0xE000, 0xFFFF}},
	{{0x10000, utf8.MaxRune}},
}

// WidthSpans are the runes of lo..hi whose UTF-8 encoding is w bytes.
func WidthSpans(lo, hi rune, w int) [][2]rune {
	var out [][2]rune
	for _, b := range utf8Bands[w] {
		if l, h := max(lo, b[0]), min(hi, b[1]); l <= h {
			out = append(out, [2]rune{l, h})
		}
	}
	return out
}

// PickRune is a rune of spans drawn by pick: half the time one of
// printable ASCII (0x20..0x7e) when the spans hold some, else any, each
// as likely.
func PickRune(spans [][2]rune, pick func(int) int) rune {
	if len(spans) == 1 {
		if lo, hi := max(spans[0][0], 0x20), min(spans[0][1], 0x7e); lo <= hi && pick(2) == 0 {
			return lo + rune(pick(int(hi-lo+1)))
		}
	}
	total := 0
	for _, s := range spans {
		total += int(s[1] - s[0] + 1)
	}
	k := pick(total)
	for _, s := range spans {
		if size := int(s[1] - s[0] + 1); k >= size {
			k -= size
			continue
		}
		return s[0] + rune(k)
	}
	panic("check: internal compiler error: PickRune past its spans")
}

func surrogateOnly(lo, hi rune) bool { return lo >= 0xD800 && hi <= 0xDFFF }

// errPatternTooLarge stops the NFA's construction past patternMaxNFA.
var errPatternTooLarge = &PatternError{Offset: 0, Msg: "the pattern's automaton is too large; write a smaller pattern"}

// CompilePattern compiles src (types.md, Refinements 11).
func CompilePattern(src string) (p *Matcher, err error) {
	if !utf8.ValidString(src) {
		return nil, &PatternError{Offset: 0, Msg: "a pattern is UTF-8 text"}
	}
	ps := &patParser{src: src}
	n, perr := ps.parse()
	if perr != nil {
		return nil, perr
	}
	defer func() {
		if r := recover(); r != nil {
			if r != errPatternTooLarge {
				panic(r)
			}
			p, err = nil, errPatternTooLarge
		}
	}()
	b := &nfaBuilder{}
	start, end := b.build(n)
	b.states[end].accept = true
	d, derr := b.determinize(start)
	if derr != nil {
		return nil, derr
	}
	d.Source = src
	return d.minimize(), nil
}

// The parser: a pattern to a tree of pnodes.

type pkind int

const (
	pEmpty  pkind = iota
	pClass        // one rune of Ranges
	pCat          // Subs in turn
	pAlt          // one of Subs
	pRepeat       // Subs[0], Min to Max times; Max -1 is no bound
)

type pnode struct {
	kind     pkind
	ranges   []PatternEdge // To unused
	subs     []*pnode
	min, max int
}

type patParser struct {
	src string
	i   int
}

func (ps *patParser) fail(at int, format string, args ...any) error {
	return &PatternError{Offset: at, Msg: fmt.Sprintf(format, args...)}
}

func (ps *patParser) parse() (*pnode, error) {
	if strings.HasPrefix(ps.src, "^") {
		ps.i = 1
	}
	n, err := ps.alt(0)
	if err != nil {
		return nil, err
	}
	if ps.i == len(ps.src)-1 && ps.src[ps.i] == '$' {
		ps.i++
	}
	if ps.i < len(ps.src) {
		if ps.src[ps.i] == ')' {
			return nil, ps.fail(ps.i, "this ) closes no group")
		}
		return nil, ps.fail(ps.i, "unexpected %q", ps.src[ps.i])
	}
	return n, nil
}

// atEnd reports the end of the pattern, or a $ that ends it.
func (ps *patParser) atEnd() bool {
	return ps.i >= len(ps.src) || ps.src[ps.i] == '$' && ps.i == len(ps.src)-1
}

func (ps *patParser) alt(depth int) (*pnode, error) {
	var subs []*pnode
	for {
		c, err := ps.cat(depth)
		if err != nil {
			return nil, err
		}
		subs = append(subs, c)
		if ps.i < len(ps.src) && ps.src[ps.i] == '|' {
			ps.i++
			continue
		}
		break
	}
	if len(subs) == 1 {
		return subs[0], nil
	}
	return &pnode{kind: pAlt, subs: subs}, nil
}

func (ps *patParser) cat(depth int) (*pnode, error) {
	var subs []*pnode
	for !ps.atEnd() {
		c := ps.src[ps.i]
		if c == '|' || c == ')' && depth > 0 {
			break
		}
		if c == ')' {
			return nil, ps.fail(ps.i, "this ) closes no group")
		}
		atom, err := ps.atom(depth)
		if err != nil {
			return nil, err
		}
		atom, err = ps.repeats(atom)
		if err != nil {
			return nil, err
		}
		subs = append(subs, atom)
	}
	if len(subs) == 1 {
		return subs[0], nil
	}
	if len(subs) == 0 {
		return &pnode{kind: pEmpty}, nil
	}
	return &pnode{kind: pCat, subs: subs}, nil
}

func (ps *patParser) repeats(atom *pnode) (*pnode, error) {
	repeated := false
	for ps.i < len(ps.src) {
		at := ps.i
		min, max := 0, 0
		switch ps.src[ps.i] {
		case '*':
			min, max = 0, -1
			ps.i++
		case '+':
			min, max = 1, -1
			ps.i++
		case '?':
			min, max = 0, 1
			ps.i++
		case '{':
			var ok bool
			var err error
			min, max, ok, err = ps.counts()
			if err != nil {
				return nil, err
			}
			if !ok {
				return nil, ps.fail(at, "a { starts a repeat {n}, {n,} or {n,m}; write \\{ for the rune")
			}
		default:
			return atom, nil
		}
		if repeated {
			return nil, ps.fail(at, "a repeat of a repeat; group it first: (…)*")
		}
		repeated = true
		if ps.i < len(ps.src) && ps.src[ps.i] == '?' {
			ps.i++ // lazy: the same whole-value match
		}
		atom = &pnode{kind: pRepeat, subs: []*pnode{atom}, min: min, max: max}
	}
	return atom, nil
}

// counts reads {n}, {n,} or {n,m} at ps.i; ok is false when the { starts
// none of them.
func (ps *patParser) counts() (min, max int, ok bool, err error) {
	at := ps.i
	j := at + 1
	num := func() (int, bool) {
		k := j
		for j < len(ps.src) && ps.src[j] >= '0' && ps.src[j] <= '9' {
			j++
		}
		if k == j {
			return 0, false
		}
		n, err := strconv.Atoi(ps.src[k:j])
		if err != nil || n > patternMaxRepeat {
			return patternMaxRepeat + 1, true
		}
		return n, true
	}
	min, ok = num()
	if !ok {
		return 0, 0, false, nil
	}
	max = min
	if j < len(ps.src) && ps.src[j] == ',' {
		j++
		if j < len(ps.src) && ps.src[j] == '}' {
			max = -1
		} else if max, ok = num(); !ok {
			return 0, 0, false, nil
		}
	}
	if j >= len(ps.src) || ps.src[j] != '}' {
		return 0, 0, false, nil
	}
	if min > patternMaxRepeat || max > patternMaxRepeat {
		return 0, 0, false, ps.fail(at, "a repeat counts at most %d", patternMaxRepeat)
	}
	if max >= 0 && max < min {
		return 0, 0, false, ps.fail(at, "a repeat {%d,%d} counts down", min, max)
	}
	ps.i = j + 1
	return min, max, true, nil
}

var (
	digitRanges = []PatternEdge{{Lo: '0', Hi: '9'}}
	wordRanges  = []PatternEdge{{Lo: '0', Hi: '9'}, {Lo: 'A', Hi: 'Z'}, {Lo: '_', Hi: '_'}, {Lo: 'a', Hi: 'z'}}
	spaceRanges = []PatternEdge{{Lo: '\t', Hi: '\n'}, {Lo: '\f', Hi: '\r'}, {Lo: ' ', Hi: ' '}}
	anyRanges   = []PatternEdge{{Lo: 0, Hi: '\n' - 1}, {Lo: '\n' + 1, Hi: utf8.MaxRune}}
)

func (ps *patParser) atom(depth int) (*pnode, error) {
	at := ps.i
	switch c := ps.src[ps.i]; c {
	case '(':
		ps.i++
		if strings.HasPrefix(ps.src[ps.i:], "?:") {
			ps.i += 2
		} else if strings.HasPrefix(ps.src[ps.i:], "?") {
			return nil, ps.fail(at, "a pattern takes no flags or named groups: (?…) other than (?:…)")
		}
		n, err := ps.alt(depth + 1)
		if err != nil {
			return nil, err
		}
		if ps.i >= len(ps.src) || ps.src[ps.i] != ')' {
			return nil, ps.fail(at, "this ( is not closed")
		}
		ps.i++
		return n, nil
	case '[':
		return ps.class()
	case '.':
		ps.i++
		return &pnode{kind: pClass, ranges: anyRanges}, nil
	case '*', '+', '?':
		return nil, ps.fail(at, "%c repeats nothing", c)
	case '{':
		return nil, ps.fail(at, "a { starts a repeat {n}, {n,} or {n,m}; write \\{ for the rune")
	case '^':
		return nil, ps.fail(at, "^ stands only first: a pattern matches the whole value")
	case '$':
		return nil, ps.fail(at, "$ stands only last: a pattern matches the whole value")
	case '\\':
		rs, err := ps.escape(false)
		if err != nil {
			return nil, err
		}
		return &pnode{kind: pClass, ranges: rs}, nil
	}
	r, n := utf8.DecodeRuneInString(ps.src[ps.i:])
	ps.i += n
	return &pnode{kind: pClass, ranges: []PatternEdge{{Lo: r, Hi: r}}}, nil
}

// escape reads the escape at ps.i: its ranges.
func (ps *patParser) escape(inClass bool) ([]PatternEdge, error) {
	at := ps.i
	ps.i++
	if ps.i >= len(ps.src) {
		return nil, ps.fail(at, "a \\ ends the pattern")
	}
	c := ps.src[ps.i]
	ps.i++
	one := func(r rune) []PatternEdge { return []PatternEdge{{Lo: r, Hi: r}} }
	switch c {
	case 'd':
		return digitRanges, nil
	case 'D':
		return complementRanges(digitRanges), nil
	case 'w':
		return wordRanges, nil
	case 'W':
		return complementRanges(wordRanges), nil
	case 's':
		return spaceRanges, nil
	case 'S':
		return complementRanges(spaceRanges), nil
	case 't':
		return one('\t'), nil
	case 'n':
		return one('\n'), nil
	case 'r':
		return one('\r'), nil
	case 'f':
		return one('\f'), nil
	case 'v':
		return one('\v'), nil
	}
	if c >= '!' && c <= '/' || c >= ':' && c <= '@' || c >= '[' && c <= '`' || c >= '{' && c <= '~' {
		return one(rune(c)), nil
	}
	return nil, ps.fail(at, "the escape \\%c is outside the pattern subset: escapes are \\d \\D \\w \\W \\s \\S \\t \\n \\r \\f \\v and a backslash before punctuation", rune(c))
}

func (ps *patParser) class() (*pnode, error) {
	at := ps.i
	ps.i++
	negate := false
	if ps.i < len(ps.src) && ps.src[ps.i] == '^' {
		negate = true
		ps.i++
	}
	var rs []PatternEdge
	first := true
	for {
		if ps.i >= len(ps.src) {
			return nil, ps.fail(at, "this [ is not closed")
		}
		c := ps.src[ps.i]
		if c == ']' && !first {
			ps.i++
			break
		}
		if c == '[' && strings.HasPrefix(ps.src[ps.i:], "[:") {
			return nil, ps.fail(ps.i, "a pattern takes no POSIX class [:…:]; write the runes")
		}
		lo, loSet, err := ps.classAtom()
		if err != nil {
			return nil, err
		}
		first = false
		if loSet != nil {
			rs = append(rs, loSet...)
			continue
		}
		if ps.i+1 < len(ps.src) && ps.src[ps.i] == '-' && ps.src[ps.i+1] != ']' {
			dash := ps.i
			ps.i++
			hi, hiSet, err := ps.classAtom()
			if err != nil {
				return nil, err
			}
			if hiSet != nil {
				return nil, ps.fail(dash, "a range ends at a rune, not a class")
			}
			if hi < lo {
				return nil, ps.fail(dash, "the range %c-%c runs backwards", lo, hi)
			}
			rs = append(rs, PatternEdge{Lo: lo, Hi: hi})
			continue
		}
		rs = append(rs, PatternEdge{Lo: lo, Hi: lo})
	}
	rs = normalizeRanges(rs)
	if negate {
		rs = complementRanges(rs)
	}
	usable := false
	for _, r := range rs {
		usable = usable || !surrogateOnly(r.Lo, r.Hi)
	}
	if !usable {
		return nil, ps.fail(at, "this class holds no rune a string can hold")
	}
	return &pnode{kind: pClass, ranges: rs}, nil
}

// classAtom reads one rune of a class, or an escape's ranges.
func (ps *patParser) classAtom() (rune, []PatternEdge, error) {
	if ps.src[ps.i] == '\\' {
		at := ps.i
		rs, err := ps.escape(true)
		if err != nil {
			return 0, nil, err
		}
		if len(rs) == 1 && rs[0].Lo == rs[0].Hi && ps.src[at+1] != 'd' && ps.src[at+1] != 'w' && ps.src[at+1] != 's' {
			return rs[0].Lo, nil, nil
		}
		return 0, rs, nil
	}
	r, n := utf8.DecodeRuneInString(ps.src[ps.i:])
	ps.i += n
	return r, nil, nil
}

// normalizeRanges sorts rs and merges overlapping and adjacent ranges.
func normalizeRanges(rs []PatternEdge) []PatternEdge {
	rs = append([]PatternEdge(nil), rs...)
	sort.Slice(rs, func(i, j int) bool { return rs[i].Lo < rs[j].Lo })
	var out []PatternEdge
	for _, r := range rs {
		if n := len(out); n > 0 && r.Lo <= out[n-1].Hi+1 {
			out[n-1].Hi = max(out[n-1].Hi, r.Hi)
			continue
		}
		out = append(out, PatternEdge{Lo: r.Lo, Hi: r.Hi})
	}
	return out
}

// complementRanges is every rune 0..MaxRune that rs does not hold.
func complementRanges(rs []PatternEdge) []PatternEdge {
	rs = normalizeRanges(rs)
	var out []PatternEdge
	next := rune(0)
	for _, r := range rs {
		if r.Lo > next {
			out = append(out, PatternEdge{Lo: next, Hi: r.Lo - 1})
		}
		next = r.Hi + 1
	}
	if next <= utf8.MaxRune {
		out = append(out, PatternEdge{Lo: next, Hi: utf8.MaxRune})
	}
	return out
}

// The automaton: a Thompson NFA, then the subset construction over the
// runes split at every edge's ends, then Moore's minimisation.

type nfaState struct {
	eps    []int
	ranges []PatternEdge // To is the target
	accept bool
}

type nfaBuilder struct{ states []nfaState }

func (b *nfaBuilder) add() int {
	if len(b.states) > patternMaxNFA {
		panic(errPatternTooLarge)
	}
	b.states = append(b.states, nfaState{})
	return len(b.states) - 1
}

// build is the fragment of n: its start and end states.
func (b *nfaBuilder) build(n *pnode) (int, int) {
	switch n.kind {
	case pEmpty:
		s := b.add()
		return s, s
	case pClass:
		s, e := b.add(), b.add()
		for _, r := range n.ranges {
			b.states[s].ranges = append(b.states[s].ranges, PatternEdge{Lo: r.Lo, Hi: r.Hi, To: e})
		}
		return s, e
	case pCat:
		s, e := b.build(n.subs[0])
		for _, sub := range n.subs[1:] {
			s2, e2 := b.build(sub)
			b.states[e].eps = append(b.states[e].eps, s2)
			e = e2
		}
		return s, e
	case pAlt:
		s, e := b.add(), b.add()
		for _, sub := range n.subs {
			s2, e2 := b.build(sub)
			b.states[s].eps = append(b.states[s].eps, s2)
			b.states[e2].eps = append(b.states[e2].eps, e)
		}
		return s, e
	}
	// pRepeat: Min copies, then Max-Min optional copies or a loop.
	s := b.add()
	e := s
	for i := 0; i < n.min; i++ {
		s2, e2 := b.build(n.subs[0])
		b.states[e].eps = append(b.states[e].eps, s2)
		e = e2
	}
	if n.max < 0 {
		s2, e2 := b.build(n.subs[0])
		out := b.add()
		b.states[e].eps = append(b.states[e].eps, s2, out)
		b.states[e2].eps = append(b.states[e2].eps, s2, out)
		return s, out
	}
	end := b.add()
	for i := n.min; i < n.max; i++ {
		s2, e2 := b.build(n.subs[0])
		b.states[e].eps = append(b.states[e].eps, s2, end)
		e = e2
	}
	b.states[e].eps = append(b.states[e].eps, end)
	return s, end
}

// closure is the sorted epsilon closure of set.
func (b *nfaBuilder) closure(set []int) []int {
	seen := map[int]bool{}
	stack := append([]int(nil), set...)
	for len(stack) > 0 {
		q := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if seen[q] {
			continue
		}
		seen[q] = true
		stack = append(stack, b.states[q].eps...)
	}
	out := make([]int, 0, len(seen))
	for q := range seen {
		out = append(out, q)
	}
	sort.Ints(out)
	return out
}

func setKey(set []int) string {
	var sb strings.Builder
	for _, q := range set {
		sb.WriteString(strconv.Itoa(q))
		sb.WriteByte(',')
	}
	return sb.String()
}

func (b *nfaBuilder) determinize(start int) (*Matcher, error) {
	p := &Matcher{}
	index := map[string]int{}
	var sets [][]int
	addSet := func(set []int) int {
		k := setKey(set)
		if i, ok := index[k]; ok {
			return i
		}
		index[k] = len(sets)
		sets = append(sets, set)
		accept := false
		for _, q := range set {
			accept = accept || b.states[q].accept
		}
		p.States = append(p.States, PatternState{Accept: accept})
		return len(sets) - 1
	}
	addSet(b.closure([]int{start}))
	for i := 0; i < len(sets); i++ {
		if len(sets) > patternMaxStates {
			return nil, &PatternError{Offset: 0, Msg: fmt.Sprintf("the pattern's automaton needs more than %d states; write a smaller pattern", patternMaxStates)}
		}
		// Split the runes at every edge's ends.
		var cuts []rune
		var edges []PatternEdge
		for _, q := range sets[i] {
			for _, e := range b.states[q].ranges {
				edges = append(edges, e)
				cuts = append(cuts, e.Lo, e.Hi+1)
			}
		}
		sort.Slice(cuts, func(a, c int) bool { return cuts[a] < cuts[c] })
		var out []PatternEdge
		for k := 0; k+1 < len(cuts); k++ {
			lo, hi := cuts[k], cuts[k+1]-1
			if lo > hi {
				continue
			}
			var targets []int
			for _, e := range edges {
				if e.Lo <= lo && hi <= e.Hi {
					targets = append(targets, e.To)
				}
			}
			if len(targets) == 0 {
				continue
			}
			to := addSet(b.closure(targets))
			if n := len(out); n > 0 && out[n-1].To == to && out[n-1].Hi+1 == lo {
				out[n-1].Hi = hi
				continue
			}
			out = append(out, PatternEdge{Lo: lo, Hi: hi, To: to})
		}
		p.States[i].Edges = out
	}
	return p, nil
}

// minimize merges the states no input tells apart (Moore) and drops the
// states that reach no accepting state, keeping the start as state 0.
func (p *Matcher) minimize() *Matcher {
	// Live states: those that reach an accepting state.
	live := make([]bool, len(p.States))
	for q, s := range p.States {
		live[q] = s.Accept
	}
	for changed := true; changed; {
		changed = false
		for q, s := range p.States {
			if live[q] {
				continue
			}
			for _, e := range s.Edges {
				if live[e.To] {
					live[q], changed = true, true
					break
				}
			}
		}
	}
	// Classes: by acceptance, then refined by the classes of the targets
	// over the same rune spans.
	class := make([]int, len(p.States))
	for q, s := range p.States {
		if !live[q] {
			class[q] = -1
		} else if s.Accept {
			class[q] = 1
		}
	}
	signature := func(q int) string {
		var sb strings.Builder
		sb.WriteString(strconv.Itoa(class[q]))
		for _, e := range p.liveEdges(q, live, class) {
			fmt.Fprintf(&sb, "|%d-%d:%d", e.Lo, e.Hi, e.To)
		}
		return sb.String()
	}
	for {
		ids := map[string]int{}
		next := make([]int, len(p.States))
		for q := range p.States {
			if !live[q] {
				next[q] = -1
				continue
			}
			k := signature(q)
			if _, ok := ids[k]; !ok {
				ids[k] = len(ids)
			}
			next[q] = ids[k]
		}
		same := true
		count := map[int]bool{}
		oldCount := map[int]bool{}
		for q := range p.States {
			count[next[q]] = true
			oldCount[class[q]] = true
		}
		same = len(count) == len(oldCount)
		class = next
		if same {
			break
		}
	}
	// Renumber in the order a walk from the start meets the classes, so the
	// start is 0 and the numbering is stable.
	out := &Matcher{Source: p.Source}
	if !live[0] {
		out.States = []PatternState{{}}
		return out
	}
	number := map[int]int{}
	var order []int // a representative state per class
	visit := func(q int) int {
		if n, ok := number[class[q]]; ok {
			return n
		}
		number[class[q]] = len(order)
		order = append(order, q)
		return len(order) - 1
	}
	visit(0)
	for i := 0; i < len(order); i++ {
		q := order[i]
		var edges []PatternEdge
		for _, e := range p.liveEdges(q, live, class) {
			to := -1
			for r, c := range class {
				if c == e.To {
					to = visit(r)
					break
				}
			}
			edges = append(edges, PatternEdge{Lo: e.Lo, Hi: e.Hi, To: to})
		}
		out.States = append(out.States, PatternState{Accept: p.States[q].Accept, Edges: edges})
	}
	return out
}

// liveEdges are q's edges to live states with each target replaced by its
// class, adjacent spans of one class merged.
func (p *Matcher) liveEdges(q int, live []bool, class []int) []PatternEdge {
	var out []PatternEdge
	for _, e := range p.States[q].Edges {
		if !live[e.To] {
			continue
		}
		to := class[e.To]
		if n := len(out); n > 0 && out[n-1].To == to && out[n-1].Hi+1 == e.Lo {
			out[n-1].Hi = e.Hi
			continue
		}
		out = append(out, PatternEdge{Lo: e.Lo, Hi: e.Hi, To: to})
	}
	return out
}
