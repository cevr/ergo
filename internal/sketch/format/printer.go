package format

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"ergo.local/prototype/internal/sketch/syntax"
)

// outLine is one output line: its indentation (tabs) and its cells. Every
// cell but the last is padded to the width of its column block. indent is
// -1 for the continuation of a multi-line raw string or block comment,
// which is never re-indented.
type outLine struct {
	indent int
	cells  []string
}

type printer struct {
	file     *syntax.File
	comments []*syntax.Comment
	ci       int // next comment to print

	lines   []outLine
	cells   []string
	cell    strings.Builder
	started bool // the current line has an item
	indent  int

	pendingNL    int  // line breaks before the next item
	pendingSpace bool // a blank before the next item on the same line
	noBlank      bool // the next line break is never a blank line

	lastEnd syntax.Pos // end of the last printed source item (token or comment)
	lastTok syntax.Pos // end of the last printed source token

	walked map[*syntax.BinaryExpr]walkResult // walkBinary, memoised

	// listIndented reports that the innermost list being printed has
	// already broken a line before the current item, so the item has a
	// continuation level (go/printer's ws == ignore in parameters).
	listIndented bool
}

type walkResult struct {
	has4, has5 bool
	maxProblem int
}

func newPrinter(f *syntax.File) *printer {
	p := &printer{file: f, lastEnd: syntax.NoPos, lastTok: syntax.NoPos, walked: map[*syntax.BinaryExpr]walkResult{}}
	for _, g := range f.Comments {
		p.comments = append(p.comments, g.List...)
	}
	return p
}

func (p *printer) line(pos syntax.Pos) int { return p.file.Line(pos) }

// breakBefore reports whether the source has a line break between the last
// printed token and pos.
func (p *printer) breakBefore(pos syntax.Pos) bool {
	return p.lastTok.IsValid() && p.line(pos) > p.line(p.lastTok)
}

// ---------------------------------------------------------------- whitespace requests

func (p *printer) space() { p.pendingSpace = true }

func (p *printer) newline() {
	if p.pendingNL == 0 {
		p.pendingNL = 1
	}
}

// newlineNoBlank requests a line break that is never a blank line (after
// an opening brace, before a closing one).
func (p *printer) newlineNoBlank() {
	p.newline()
	p.noBlank = true
}

// gap is the whitespace before the token at pos in a gap whose line break
// the source decides: a line break if the source has one, else ws.
func (p *printer) gap(pos syntax.Pos, blank bool) bool {
	if p.breakBefore(pos) {
		p.newline()
		return true
	}
	if blank {
		p.space()
	}
	return false
}

// ---------------------------------------------------------------- output

// emit applies the pending whitespace before an item at pos (NoPos for a
// synthesized token).
func (p *printer) emit(pos syntax.Pos) {
	if p.pendingNL > 0 && (p.started || len(p.lines) > 0) {
		n := 1
		if pos.IsValid() && p.lastEnd.IsValid() && !p.noBlank && p.line(pos)-p.line(p.lastEnd) >= 2 {
			n = 2
		}
		p.endLine()
		if n == 2 {
			p.lines = append(p.lines, outLine{})
		}
	}
	p.pendingNL = 0
	p.noBlank = false
	if !p.started {
		p.started = true
		p.pendingSpace = false
		p.lineIndent()
		return
	}
	if p.pendingSpace {
		p.cell.WriteByte(' ')
	}
	p.pendingSpace = false
}

func (p *printer) lineIndent() {
	p.lines = append(p.lines, outLine{indent: p.indent})
	p.lines[len(p.lines)-1].cells = nil
}

func (p *printer) endLine() {
	if !p.started {
		return
	}
	p.cells = append(p.cells, p.cell.String())
	p.cell.Reset()
	for len(p.cells) > 1 && p.cells[len(p.cells)-1] == "" {
		p.cells = p.cells[:len(p.cells)-1]
	}
	p.lines[len(p.lines)-1].cells = p.cells
	p.cells = nil
	p.started = false
}

// sep ends the current cell: the next text aligns with the cells of the
// same column on the neighbouring lines.
func (p *printer) sep() {
	p.cells = append(p.cells, strings.TrimRight(p.cell.String(), " "))
	p.cell.Reset()
}

// lastByte is the last byte written on the current line, or 0.
func (p *printer) lastByte() byte {
	if s := p.cell.String(); s != "" {
		return s[len(s)-1]
	}
	for i := len(p.cells) - 1; i >= 0; i-- {
		if s := p.cells[i]; s != "" {
			return s[len(s)-1]
		}
	}
	return 0
}

// mergers are the byte pairs that would lex as one token, or start a
// comment, if written without a blank between them.
var mergers = sync.OnceValue(func() map[[2]byte]bool {
	return map[[2]byte]bool{
		{'-', '-'}: true, {'+', '+'}: true, {'&', '&'}: true, {'&', '^'}: true, {'|', '|'}: true,
		{'<', '-'}: true, {'<', '<'}: true, {'>', '>'}: true, {'=', '='}: true, {'!', '='}: true,
		{'<', '='}: true, {'>', '='}: true, {':', '='}: true, {'.', '.'}: true, {'/', '/'}: true,
		{'/', '*'}: true, {'-', '='}: true, {'+', '='}: true, {'*', '='}: true, {'/', '='}: true,
		{'%', '='}: true, {'&', '='}: true, {'|', '='}: true, {'^', '='}: true, {'*', '/'}: true,
	}
})

// write appends text; a text with line breaks (a raw string or block
// comment) continues on lines that are never re-indented.
func (p *printer) write(text string) {
	if text == "" {
		return
	}
	if last := p.lastByte(); last != 0 && mergers()[[2]byte{last, text[0]}] {
		p.cell.WriteByte(' ')
	}
	for {
		i := strings.IndexByte(text, '\n')
		if i < 0 {
			p.cell.WriteString(text)
			return
		}
		p.cell.WriteString(text[:i])
		p.endLine()
		p.started = true
		p.lines = append(p.lines, outLine{indent: -1})
		text = text[i+1:]
	}
}

// token prints a source token at pos after the comments before it.
func (p *printer) token(pos syntax.Pos, text string) {
	p.tokenEnd(pos, text, pos+syntax.Pos(len(text)))
}

func (p *printer) tokenEnd(pos syntax.Pos, text string, end syntax.Pos) {
	p.flushComments(pos)
	if p.pendingSpace && p.pendingNL == 0 && p.lastEnd > p.lastTok && p.lastTok.IsValid() {
		// After a block comment on the same line, go/printer writes no
		// blank before `,`, nor before `)` or `]` unless the comment
		// directly follows the opener: `f(a /* x */, b)`, `f( /* x */ )`.
		// The middle and tail segments of an interpolated string start
		// with the `)` that closes the interpolation, so they count as `)`.
		opener := p.file.Src[p.lastTok-1]
		switch {
		case text == ",",
			strings.HasPrefix(text, ")") && opener != '(',
			text == "]" && opener != '[':
			p.pendingSpace = false
		}
	}
	p.emit(pos)
	p.write(text)
	p.lastEnd, p.lastTok = end, end
}

// synth prints a token whose position the tree does not record: a comma,
// a `|`, the `]` of an array type, the `.` of a selector. When the next
// source token after the last printed one is that token, it is printed at
// its source position, so the comments before it stay before it and the
// comments after it stay after it (rustfmt recovers comment positions from
// source spans the same way). Otherwise the token is not in the source,
// such as a comma added before a closer on its own line.
func (p *printer) synth(text string) {
	if pos, ok := p.sourceToken(text); ok {
		p.token(pos, text)
		return
	}
	p.emit(syntax.NoPos)
	p.write(text)
}

// sourceToken finds text as the next source token after the last printed
// token, skipping blanks and comments.
func (p *printer) sourceToken(text string) (syntax.Pos, bool) {
	if !p.lastTok.IsValid() {
		return syntax.NoPos, false
	}
	src := p.file.Src
	i := int(p.lastTok)
	ci := sort.Search(len(p.comments), func(k int) bool { return int(p.comments[k].Slash) >= i })
	for i < len(src) {
		switch src[i] {
		case ' ', '\t', '\r', '\n':
			i++
			continue
		}
		for ci < len(p.comments) && int(p.comments[ci].Slash) < i {
			ci++
		}
		if ci < len(p.comments) && int(p.comments[ci].Slash) == i {
			i = int(p.comments[ci].EndPos)
			continue
		}
		break
	}
	if !strings.HasPrefix(src[i:], text) {
		return syntax.NoPos, false
	}
	if end := i + len(text); isWordByte(text[len(text)-1]) && end < len(src) && isWordByte(src[end]) {
		return syntax.NoPos, false
	}
	return syntax.Pos(i), true
}

func isWordByte(c byte) bool {
	return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80
}

// ---------------------------------------------------------------- comments

// flushComments prints every comment before pos.
func (p *printer) flushComments(pos syntax.Pos) {
	for p.ci < len(p.comments) && p.comments[p.ci].Slash < pos {
		p.comment(pos)
	}
}

// flushClauseComments prints, at the current (clause body) indentation, the
// comments before pos up to the first one that a blank line separates from
// what precedes it; that one and the rest belong to the next clause.
func (p *printer) flushClauseComments(pos syntax.Pos) {
	for p.ci < len(p.comments) && p.comments[p.ci].Slash < pos {
		c := p.comments[p.ci]
		if p.lastEnd.IsValid() && p.line(c.Slash)-p.line(p.lastEnd) >= 2 {
			return
		}
		p.comment(pos)
	}
}

func (p *printer) comment(limit syntax.Pos) {
	c := p.comments[p.ci]
	next := limit
	if p.ci+1 < len(p.comments) && p.comments[p.ci+1].Slash < limit {
		next = p.comments[p.ci+1].Slash
	}
	p.ci++
	isLine := strings.HasPrefix(c.Text, "//")
	text := c.Text
	if isLine {
		// A line comment ends at a newline; trailing blanks, and a carriage
		// return the scanner kept because a blank followed it, are dropped,
		// so the output reads back as the same comment.
		text = strings.TrimRight(text, " \t\r")
	}
	trailing := p.lastEnd.IsValid() && p.line(c.Slash) == p.line(p.lastEnd)
	switch {
	case trailing && p.pendingNL > 0:
		// A comment that ends a line aligns with its neighbours.
		nl, noBlank := p.pendingNL, p.noBlank
		p.pendingNL, p.pendingSpace = 0, false
		p.emit(c.Slash)
		p.sep()
		p.cell.WriteString(text)
		p.pendingNL, p.noBlank = nl, noBlank
	case trailing:
		p.space()
		p.emit(c.Slash)
		p.cell.WriteString(text)
		if isLine {
			p.newline()
		} else {
			p.space()
		}
	default:
		if p.lastEnd.IsValid() {
			p.newline()
		}
		p.emit(c.Slash)
		p.write(text)
		if isLine || p.line(next) > p.line(c.EndPos) {
			p.newline()
		} else {
			p.space()
		}
	}
	p.lastEnd = c.EndPos
}

// ---------------------------------------------------------------- rendering

// render aligns the cells of each column block and joins the lines. A
// column block is a run of adjacent lines with the same indentation that
// all have a cell in that column followed by another cell, as in
// text/tabwriter; blocks of a column nest inside the blocks of the column
// before it. Cells are padded with spaces to the block's widest cell plus
// one; a block of empty cells takes no width.
//
// limit, when positive, bounds the output in bytes: the size is computed
// from the cells and their padding before any text is built, and a larger
// output is a *LimitError with no text, so an input whose alignment would
// multiply into a large output never allocates it.
func (p *printer) render(limit int) (string, error) {
	p.flushComments(p.file.EOF + 1)
	p.endLine()
	lines := p.lines
	widths := make([][]int, len(lines))
	p.align(lines, widths, 0, len(lines), 0)
	lineLen := func(i int) int {
		l := lines[i]
		n := max(l.indent, 0)
		for j, c := range l.cells {
			n += len(c)
			if j < len(l.cells)-1 {
				if w := widths[i][j]; w > 0 {
					n += w - utf8.RuneCountInString(c)
				}
			}
		}
		return n
	}
	// Leading empty lines are dropped: no output starts with a blank line.
	first := 0
	for first < len(lines) && lineLen(first) == 0 {
		first++
	}
	size := 0
	for i := first; i < len(lines); i++ {
		size += lineLen(i) + 1
		if limit > 0 && size > limit {
			return "", &LimitError{Limit: limit}
		}
	}
	var b strings.Builder
	b.Grow(size)
	for i := first; i < len(lines); i++ {
		l := lines[i]
		if l.indent > 0 {
			b.WriteString(strings.Repeat("\t", l.indent))
		}
		for j, c := range l.cells {
			b.WriteString(c)
			if j < len(l.cells)-1 {
				if w := widths[i][j]; w > 0 {
					b.WriteString(strings.Repeat(" ", w-utf8.RuneCountInString(c)))
				}
			}
		}
		b.WriteByte('\n')
	}
	return b.String(), nil
}

// LimitError is the refusal of an output larger than the caller's bound.
type LimitError struct{ Limit int }

func (e *LimitError) Error() string {
	return fmt.Sprintf("formatted source exceeds the %d-byte output limit", e.Limit)
}

// Alignment sections follow go/printer's exprList heuristic on every
// column: a cell whose width differs from the geometric mean of its
// section's non-empty widths by a factor of alignRatio or more starts a new
// section, unless both it and the previous non-empty cell are at most
// alignSmall wide. One long name therefore does not pad every short
// neighbour to its width.
//
// An empty cell (the type of `b = 2` in a var group) is padded to its
// section's width, so it may share a section only while that width is at
// most alignSmall: gofmt's `b            = 2` under typed neighbours stays,
// and a run of empty cells never inherits a long cell's width. A section
// that holds an empty cell ends before a cell wider than alignSmall.
//
// The section bound: in a section whose widest cell W is wider than
// alignSmall, every non-empty cell is wider than W/alignRatio, and no cell
// is empty. A cell's padding is therefore at most alignSmall+1 columns, or
// less than 1.5 times its own width plus one. Each aligned column adds at
// most a constant plus a constant factor of its own text, so the output is
// linear in the input for every input, not only where go/printer's
// neighbour test happens to break a taper (review r3 M1).
const (
	alignRatio = 2.5
	alignSmall = 40
)

func (p *printer) align(lines []outLine, widths [][]int, from, to, column int) {
	for i := from; i < to; {
		if len(lines[i].cells) <= column+1 {
			i++
			continue
		}
		j := i + 1
		size := func(k int) int { return utf8.RuneCountInString(lines[k].cells[column]) }
		prev := size(i)
		widest, narrowest, hasEmpty := prev, prev, prev == 0
		lnsum, count := 0.0, 0
		if prev > 0 {
			lnsum, count = math.Log(float64(prev)), 1
		}
		for j < to && len(lines[j].cells) > column+1 && lines[j].indent == lines[i].indent {
			s := size(j)
			if s == 0 {
				if widest > alignSmall {
					break
				}
				hasEmpty = true
				j++
				continue
			}
			if hasEmpty && s > alignSmall {
				break
			}
			if count > 0 && (s > alignSmall || prev > alignSmall) {
				ratio := float64(s) / math.Exp(lnsum/float64(count))
				if ratio*alignRatio <= 1 || ratio >= alignRatio {
					break
				}
			}
			if count > 0 {
				// The section bound: once any cell is wider than
				// alignSmall, every non-empty cell stays within alignRatio
				// of the widest, so a slow taper cannot carry a long
				// cell's width to short ones.
				hi, lo := max(widest, s), min(narrowest, s)
				if hi > alignSmall && float64(hi) >= alignRatio*float64(lo) {
					break
				}
				narrowest = lo
			} else {
				narrowest = s
			}
			lnsum += math.Log(float64(s))
			count++
			prev, widest = s, max(widest, s)
			j++
		}
		width := 0
		for k := i; k < j; k++ {
			width = max(width, utf8.RuneCountInString(lines[k].cells[column]))
		}
		if width > 0 {
			width++
		}
		for k := i; k < j; k++ {
			for len(widths[k]) <= column {
				widths[k] = append(widths[k], 0)
			}
			widths[k][column] = width
		}
		p.align(lines, widths, i, j, column+1)
		i = j
	}
}
