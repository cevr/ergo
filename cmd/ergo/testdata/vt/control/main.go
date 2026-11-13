// Command control is the hand-Go control of examples/vt (DOD track DT3):
// `vt replay` written as a person would write it in Go, in the dense form.
// It reads terminal output on standard input, plays it on a 24 × 80
// screen with 1 MiB of history, and prints what `vt replay` prints: the
// history and the buffer (each change of style in braces), the cursor,
// pen and title, the parser's tally and the unhandled sequences.
//
// The parser is Ghostty's: a [256][14] transition table (parse_table.zig)
// with the UTF-8 decoder in front of the ground state, the collector
// beside it, and two fast paths (a run of printable ASCII, a run of OSC
// data). It calls the screen directly; no event is built. The screen is
// Ghostty's page form: 8-byte cells with no pointer in one slice (the
// alternate buffer's rows, then a ring of pages for the main buffer and
// its history), a byte of flags per row, and a counted set of styles.
//
// TestVTAgreesWithControl (cmd/ergo) holds its output to the Ergo
// program's on both of its screens, and the DT3 receipt measures it.
package main

import (
	"bufio"
	"io"
	"os"
	"sort"
	"strconv"
	"unicode/utf8"
)

// ---- Parser ----

const (
	ground = iota
	escape
	escapeIntermediate
	csiEntry
	csiIntermediate
	csiParam
	csiIgnore
	dcsEntry
	dcsParam
	dcsIntermediate
	dcsPassthrough
	dcsIgnore
	oscString
	sosPmApcString
	nstates
)

const (
	aNone = iota
	aIgnore
	aPrint
	aExecute
	aCollect
	aParam
	aEsc
	aCsi
	aPut
	aOscPut
	aApcPut
)

type transition struct{ to, act uint8 }

var table [256][nstates]transition

func init() {
	var set [256][nstates]bool
	one := func(c, s0, s1, a int) {
		table[c][s0] = transition{uint8(s1), uint8(a)}
		set[c][s0] = true
	}
	span := func(lo, hi, s0, s1, a int) {
		for c := lo; c <= hi; c++ {
			one(c, s0, s1, a)
		}
	}
	for s := 0; s < nstates; s++ {
		one(0x18, s, ground, aExecute)
		one(0x1a, s, ground, aExecute)
		span(0x80, 0x8f, s, ground, aExecute)
		span(0x91, 0x97, s, ground, aExecute)
		one(0x99, s, ground, aExecute)
		one(0x9a, s, ground, aExecute)
		one(0x9c, s, ground, aNone)
		one(0x1b, s, escape, aNone)
		one(0x98, s, sosPmApcString, aNone)
		one(0x9e, s, sosPmApcString, aNone)
		one(0x9f, s, sosPmApcString, aNone)
		one(0x9b, s, csiEntry, aNone)
		one(0x90, s, dcsEntry, aNone)
		one(0x9d, s, oscString, aNone)
	}
	c0 := func(s, a int) { one(0x19, s, s, a); span(0, 0x17, s, s, a); span(0x1c, 0x1f, s, s, a) }
	c0(ground, aExecute)
	span(0x20, 0x7f, ground, ground, aPrint)
	s := escapeIntermediate
	c0(s, aExecute)
	span(0x20, 0x2f, s, s, aCollect)
	one(0x7f, s, s, aIgnore)
	span(0x30, 0x7e, s, ground, aEsc)
	s = sosPmApcString
	c0(s, aApcPut)
	span(0x20, 0x7f, s, s, aApcPut)
	s = escape
	c0(s, aExecute)
	one(0x7f, s, s, aIgnore)
	span(0x30, 0x4f, s, ground, aEsc)
	span(0x51, 0x57, s, ground, aEsc)
	span(0x60, 0x7e, s, ground, aEsc)
	one(0x59, s, ground, aEsc)
	one(0x5a, s, ground, aEsc)
	one(0x5c, s, ground, aEsc)
	span(0x20, 0x2f, s, escapeIntermediate, aCollect)
	one(0x58, s, sosPmApcString, aNone)
	one(0x5e, s, sosPmApcString, aNone)
	one(0x5f, s, sosPmApcString, aNone)
	one(0x50, s, dcsEntry, aNone)
	one(0x5b, s, csiEntry, aNone)
	one(0x5d, s, oscString, aNone)
	s = dcsEntry
	c0(s, aIgnore)
	one(0x7f, s, s, aIgnore)
	span(0x20, 0x2f, s, dcsIntermediate, aCollect)
	one(0x3a, s, dcsIgnore, aNone)
	span(0x30, 0x39, s, dcsParam, aParam)
	one(0x3b, s, dcsParam, aParam)
	span(0x3c, 0x3f, s, dcsParam, aCollect)
	span(0x40, 0x7e, s, dcsPassthrough, aNone)
	s = dcsIntermediate
	c0(s, aIgnore)
	span(0x20, 0x2f, s, s, aCollect)
	one(0x7f, s, s, aIgnore)
	span(0x30, 0x3f, s, dcsIgnore, aNone)
	span(0x40, 0x7e, s, dcsPassthrough, aNone)
	s = dcsIgnore
	c0(s, aIgnore)
	span(0x80, 0xff, s, s, aIgnore)
	s = dcsParam
	c0(s, aIgnore)
	span(0x30, 0x39, s, s, aParam)
	one(0x3b, s, s, aParam)
	one(0x7f, s, s, aIgnore)
	one(0x3a, s, dcsIgnore, aNone)
	span(0x3c, 0x3f, s, dcsIgnore, aNone)
	span(0x20, 0x2f, s, dcsIntermediate, aCollect)
	span(0x40, 0x7e, s, dcsPassthrough, aNone)
	s = dcsPassthrough
	c0(s, aPut)
	span(0x20, 0x7e, s, s, aPut)
	one(0x7f, s, s, aIgnore)
	span(0x80, 0xff, s, s, aPut)
	s = csiParam
	c0(s, aExecute)
	span(0x30, 0x39, s, s, aParam)
	one(0x3a, s, s, aParam)
	one(0x3b, s, s, aParam)
	one(0x7f, s, s, aIgnore)
	span(0x40, 0x7e, s, ground, aCsi)
	span(0x3c, 0x3f, s, csiIgnore, aNone)
	span(0x20, 0x2f, s, csiIntermediate, aCollect)
	s = csiIgnore
	c0(s, aExecute)
	span(0x20, 0x3f, s, s, aIgnore)
	one(0x7f, s, s, aIgnore)
	span(0x40, 0x7e, s, ground, aNone)
	s = csiIntermediate
	c0(s, aExecute)
	span(0x20, 0x2f, s, s, aCollect)
	one(0x7f, s, s, aIgnore)
	span(0x40, 0x7e, s, ground, aCsi)
	span(0x30, 0x3f, s, csiIgnore, aNone)
	s = csiEntry
	c0(s, aExecute)
	one(0x7f, s, s, aIgnore)
	span(0x40, 0x7e, s, ground, aCsi)
	one(0x3a, s, csiIgnore, aNone)
	span(0x20, 0x2f, s, csiIntermediate, aCollect)
	span(0x30, 0x39, s, csiParam, aParam)
	one(0x3b, s, csiParam, aParam)
	span(0x3c, 0x3f, s, csiParam, aCollect)
	s = oscString
	one(0x19, s, s, aIgnore)
	span(0, 0x06, s, s, aIgnore)
	span(0x08, 0x17, s, s, aIgnore)
	span(0x1c, 0x1f, s, s, aIgnore)
	span(0x20, 0xff, s, s, aOscPut)
	one(0x07, s, ground, aNone)
	for c := 0; c < 256; c++ {
		for s := 0; s < nstates; s++ {
			if !set[c][s] {
				table[c][s] = transition{uint8(s), aNone}
			}
		}
	}
}

const (
	maxParams        = 24
	maxIntermediates = 4
	maxParam         = 65535
	oscLimit         = 1024
	titleLimit       = 256
)

// parser is the parser, its collector and its decoder; it plays on scr.
type parser struct {
	state   uint8
	params  [maxParams]int
	nparams int
	digits  bool
	acc     int
	colons  uint32
	inter   [maxIntermediates]byte
	ninter  int
	osc     []byte
	length  int
	need    int
	seen    int
	cp      int
	lower   byte
	upper   byte
	scr     *screen
	notes   map[note]int
	bytes   int
	dropped int
	bad     int
	oscCut  int
}

func newParser(scr *screen) *parser {
	return &parser{lower: 0x80, upper: 0xbf, scr: scr, notes: map[note]int{}, osc: make([]byte, 0, oscLimit)}
}

func (p *parser) feed(in []byte) {
	p.bytes += len(in)
	n := len(in)
	for i := 0; i < n; {
		b := in[i]
		if p.need > 0 {
			if b < p.lower || b > p.upper {
				p.glyph(utf8.RuneError)
				p.bad++
				p.need, p.seen, p.cp, p.lower, p.upper = 0, 0, 0, 0x80, 0xbf
				continue
			}
			p.lower, p.upper = 0x80, 0xbf
			p.cp = p.cp<<6 | int(b&0x3f)
			p.seen++
			if p.seen == p.need {
				p.glyph(rune(p.cp))
				p.need, p.seen, p.cp = 0, 0, 0
			}
			i++
			continue
		}
		switch p.state {
		case ground:
			if b >= 0x80 {
				p.lead(b)
				i++
				continue
			}
			if b >= 0x20 && b < 0x7f {
				j := i + 1
				for j < n && in[j] >= 0x20 && in[j] < 0x7f {
					j++
				}
				p.scr.print(in[i:j])
				i = j
				continue
			}
		case oscString:
			if b >= 0x20 {
				j := i + 1
				for j < n && in[j] >= 0x20 {
					j++
				}
				p.keep(in[i:j])
				i = j
				continue
			}
		}
		p.step(b)
		i++
	}
}

func (p *parser) finish() {
	if p.need > 0 {
		p.need, p.seen, p.cp, p.lower, p.upper = 0, 0, 0, 0x80, 0xbf
		p.bad++
		p.glyph(utf8.RuneError)
	}
}

func (p *parser) lead(b byte) {
	switch {
	case b >= 0xc2 && b <= 0xdf:
		p.need, p.cp = 1, int(b&0x1f)
	case b >= 0xe0 && b <= 0xef:
		p.need, p.cp = 2, int(b&0x0f)
		if b == 0xe0 {
			p.lower = 0xa0
		}
		if b == 0xed {
			p.upper = 0x9f
		}
	case b >= 0xf0 && b <= 0xf4:
		p.need, p.cp = 3, int(b&0x07)
		if b == 0xf0 {
			p.lower = 0x90
		}
		if b == 0xf4 {
			p.upper = 0x8f
		}
	default:
		p.bad++
		p.glyph(utf8.RuneError)
	}
}

func (p *parser) glyph(r rune) { p.scr.printRune(r, widthOf(r)) }

func (p *parser) keep(data []byte) {
	room := oscLimit - len(p.osc)
	if len(data) <= room {
		p.osc = append(p.osc, data...)
		return
	}
	if room > 0 {
		p.osc = append(p.osc, data[:room]...)
	}
	p.oscCut += len(data) - room
}

func (p *parser) step(b byte) {
	t := table[b][p.state]
	moved := t.to != p.state
	if moved {
		switch p.state {
		case oscString:
			p.oscEnd()
		case sosPmApcString:
			p.note(note{kind: noteApc})
		}
	}
	switch t.act {
	case aPrint:
		p.scr.print([]byte{b})
	case aExecute:
		p.control(b)
	case aCollect:
		if p.ninter < maxIntermediates {
			p.inter[p.ninter] = b
			p.ninter++
		}
	case aParam:
		if b == ';' || b == ':' {
			if p.nparams < maxParams {
				p.params[p.nparams] = p.acc
				if b == ':' {
					p.colons |= 1 << p.nparams
				}
				p.nparams++
			}
			p.acc = 0
			p.digits = false
		} else {
			p.acc = min(p.acc*10+int(b-'0'), maxParam)
			p.digits = true
		}
	case aEsc:
		p.esc(b)
	case aCsi:
		if p.nparams >= maxParams {
			p.dropped++
			break
		}
		if p.digits {
			p.params[p.nparams] = p.acc
			p.nparams++
			p.digits = false
		}
		if b != 'm' && p.colons != 0 {
			p.dropped++
			break
		}
		p.csi(b)
	case aPut, aApcPut:
		p.length++
	case aOscPut:
		p.keep([]byte{b})
	}
	if moved {
		switch t.to {
		case escape, csiEntry, dcsEntry:
			p.ninter, p.nparams, p.digits, p.colons, p.acc = 0, 0, false, 0, 0
		case oscString:
			p.osc = p.osc[:0]
		case dcsPassthrough:
			p.length = 0
			if p.nparams >= maxParams {
				p.dropped++
				break
			}
			p.note(note{kind: noteDcs})
		case sosPmApcString:
			p.length = 0
		}
	}
	p.state = t.to
}

// ---- Applying ----

const (
	noteControl = iota
	noteEsc
	noteOsc
	noteDcs
	noteApc
	noteMode
	noteCsi
	noteEraseDisplay
	noteEraseLine
	noteSgr
	noteSgrGroup
	noteSgr58
)

// note is one kind of event the screen does not take.
type note struct {
	kind uint8
	n    int
	text string
}

func (n note) String() string {
	switch n.kind {
	case noteControl:
		return "control " + strconv.Itoa(n.n)
	case noteEsc:
		return "esc " + n.text
	case noteOsc:
		return "osc"
	case noteDcs:
		return "dcs"
	case noteApc:
		return "apc"
	case noteMode:
		return "mode " + strconv.Itoa(n.n)
	case noteCsi:
		return "csi " + n.text
	case noteEraseDisplay:
		return "erase display " + strconv.Itoa(n.n)
	case noteEraseLine:
		return "erase line " + strconv.Itoa(n.n)
	case noteSgr:
		return "sgr " + strconv.Itoa(n.n)
	case noteSgrGroup:
		return "sgr " + strconv.Itoa(n.n) + ":"
	default:
		return "sgr 58"
	}
}

func (p *parser) note(n note) { p.notes[n]++ }

func (p *parser) control(c byte) {
	switch c {
	case 0x07:
	case 0x08:
		p.scr.backspace()
	case 0x09:
		p.scr.tab()
	case 0x0a, 0x0b, 0x0c:
		p.scr.lineFeed()
	case 0x0d:
		p.scr.carriageReturn()
	default:
		p.note(note{kind: noteControl, n: int(c)})
	}
}

func (p *parser) oscEnd() {
	d := p.osc
	if len(d) >= 2 && (d[0] == '0' || d[0] == '2') && d[1] == ';' {
		t := d[2:]
		if len(t) > titleLimit {
			t = t[:titleLimit]
		}
		p.scr.title = string(t)
		return
	}
	p.note(note{kind: noteOsc})
}

func (p *parser) esc(final byte) {
	if p.ninter > 0 {
		p.note(note{kind: noteEsc, text: string(p.inter[:p.ninter]) + string([]byte{final})})
		return
	}
	s := p.scr
	switch final {
	case 'D':
		s.lineFeed()
	case 'E':
		s.carriageReturn()
		s.lineFeed()
	case 'M':
		s.reverseIndex()
	case '7':
		s.save()
	case '8':
		s.restore()
	case 'c':
		s.reset()
	case '\\':
	default:
		p.note(note{kind: noteEsc, text: string([]byte{final})})
	}
}

func arg(ps []int, i, def int) int {
	if i < len(ps) && ps[i] != 0 {
		return ps[i]
	}
	return def
}

func (p *parser) csi(final byte) {
	ps := p.params[:p.nparams]
	s := p.scr
	if p.ninter == 1 && p.inter[0] == '?' && (final == 'h' || final == 'l') {
		for _, m := range ps {
			switch m {
			case 6, 7, 25, 1049:
				s.setMode(m, final == 'h')
			default:
				p.note(note{kind: noteMode, n: m})
			}
		}
		return
	}
	if p.ninter > 0 {
		p.note(note{kind: noteCsi, text: string(p.inter[:p.ninter]) + string([]byte{final})})
		return
	}
	switch final {
	case 'A':
		s.up(arg(ps, 0, 1))
	case 'B':
		s.down(arg(ps, 0, 1))
	case 'C':
		s.forward(arg(ps, 0, 1))
	case 'D':
		s.back(arg(ps, 0, 1))
	case 'H', 'f':
		s.moveTo(arg(ps, 0, 1), arg(ps, 1, 1))
	case 'G':
		s.column(arg(ps, 0, 1))
	case 'd':
		s.row(arg(ps, 0, 1))
	case 'J':
		if m := arg(ps, 0, 0); m <= 2 {
			s.eraseDisplay(m)
		} else {
			p.note(note{kind: noteEraseDisplay, n: m})
		}
	case 'K':
		if m := arg(ps, 0, 0); m <= 2 {
			s.eraseLine(m)
		} else {
			p.note(note{kind: noteEraseLine, n: m})
		}
	case 'L':
		s.insertLines(arg(ps, 0, 1))
	case 'M':
		s.deleteLines(arg(ps, 0, 1))
	case '@':
		s.insertBlanks(arg(ps, 0, 1))
	case 'P':
		s.deleteChars(arg(ps, 0, 1))
	case 'X':
		s.eraseChars(arg(ps, 0, 1))
	case 'S':
		s.scrollUp(arg(ps, 0, 1))
	case 'T':
		s.shiftDown(s.top, s.bottom, arg(ps, 0, 1))
	case 'r':
		s.setRegion(arg(ps, 0, 0), arg(ps, 1, 0))
	case 'm':
		s.setPen(p.sgr(s.pen, ps))
	default:
		p.note(note{kind: noteCsi, text: string([]byte{final})})
	}
}

const (
	bold      = 1
	italic    = 2
	underline = 4
	inverse   = 8
	rgbBase   = 0x1000000
)

// style is a pen: two colours (0 the default, 1 to 256 the palette,
// rgbBase and up RGB) and the flags.
type style struct{ fg, bg, flags int32 }

var defaultStyle style

func withFlag(st style, f int32, on bool) style {
	if on {
		st.flags |= f
	} else {
		st.flags &^= f
	}
	return st
}

func rgb(r, g, b int) int32 { return int32(rgbBase + (r%256)<<16 | (g%256)<<8 | b%256) }

func groupColor(g []int) int32 {
	if len(g) == 3 && g[1] == 5 {
		return int32(g[2]%256 + 1)
	}
	if len(g) >= 5 && g[1] == 2 {
		n := len(g)
		return rgb(g[n-3], g[n-2], g[n-1])
	}
	return 0
}

func (p *parser) sgr(pen style, ps []int) style {
	if len(ps) == 0 {
		return defaultStyle
	}
	st := pen
	for i := 0; i < len(ps); {
		end := i + 1
		for end < len(ps) && (p.colons>>(end-1))&1 == 1 {
			end++
		}
		v := ps[i]
		if end > i+1 {
			g := ps[i:end]
			switch v {
			case 4:
				st = withFlag(st, underline, g[1] != 0)
			case 38:
				st.fg = groupColor(g)
			case 48:
				st.bg = groupColor(g)
			case 58:
				p.note(note{kind: noteSgr58})
			default:
				p.note(note{kind: noteSgrGroup, n: v})
			}
			i = end
			continue
		}
		if (v == 38 || v == 48) && i+2 < len(ps) && ps[i+1] == 5 {
			c := int32(ps[i+2]%256 + 1)
			if v == 38 {
				st.fg = c
			} else {
				st.bg = c
			}
			i += 3
			continue
		}
		if (v == 38 || v == 48) && i+4 < len(ps) && ps[i+1] == 2 {
			c := rgb(ps[i+2], ps[i+3], ps[i+4])
			if v == 38 {
				st.fg = c
			} else {
				st.bg = c
			}
			i += 5
			continue
		}
		switch {
		case v >= 30 && v <= 37:
			st.fg = int32(v - 30 + 1)
		case v >= 90 && v <= 97:
			st.fg = int32(v - 90 + 9)
		case v >= 40 && v <= 47:
			st.bg = int32(v - 40 + 1)
		case v >= 100 && v <= 107:
			st.bg = int32(v - 100 + 9)
		case v == 0:
			st = defaultStyle
		case v == 1:
			st = withFlag(st, bold, true)
		case v == 3:
			st = withFlag(st, italic, true)
		case v == 4:
			st = withFlag(st, underline, true)
		case v == 7:
			st = withFlag(st, inverse, true)
		case v == 22:
			st = withFlag(st, bold, false)
		case v == 23:
			st = withFlag(st, italic, false)
		case v == 24:
			st = withFlag(st, underline, false)
		case v == 27:
			st = withFlag(st, inverse, false)
		case v == 39:
			st.fg = 0
		case v == 49:
			st.bg = 0
		default:
			p.note(note{kind: noteSgr, n: v})
		}
		i++
	}
	return st
}

// ---- Width ----

var wide = [][2]int{
	{0x1100, 0x115f}, {0x2329, 0x232a}, {0x2e80, 0x303e}, {0x3040, 0xa4cf},
	{0xac00, 0xd7a3}, {0xf900, 0xfaff}, {0xfe10, 0xfe19}, {0xfe30, 0xfe6f},
	{0xff00, 0xff60}, {0xffe0, 0xffe6}, {0x1f300, 0x1f64f}, {0x1f900, 0x1f9ff},
	{0x20000, 0x2fffd}, {0x30000, 0x3fffd},
}

// widthFirst maps a code point's block of 256 to its row of widthSecond;
// row 0 is all narrow and row 1 all wide.
var (
	widthFirst  []uint8
	widthSecond [][256]bool
)

func init() {
	widthSecond = make([][256]bool, 2)
	for i := range widthSecond[1] {
		widthSecond[1][i] = true
	}
	end := 0
	for _, s := range wide {
		end = max(end, s[1]/256+1)
	}
	widthFirst = make([]uint8, end)
	for high := 0; high < end; high++ {
		base := high * 256
		var row [256]bool
		n := 0
		for _, s := range wide {
			for cp := max(s[0], base); cp <= min(s[1], base+255); cp++ {
				row[cp-base] = true
				n++
			}
		}
		switch n {
		case 0:
		case 256:
			widthFirst[high] = 1
		default:
			widthFirst[high] = uint8(len(widthSecond))
			widthSecond = append(widthSecond, row)
		}
	}
}

func widthOf(r rune) int {
	cp := int(r)
	if cp < 0x1100 || cp/256 >= len(widthFirst) {
		return 1
	}
	if widthSecond[widthFirst[cp/256]][cp%256] {
		return 2
	}
	return 1
}

// ---- Screen ----

const (
	rows       = 24
	cols       = 80
	scrollback = 1 << 20
	pageRows   = 16
	cellBytes  = 8

	flagWrap   = 1
	flagStyled = 2

	partSingle = 0
	partHead   = 1
	partTail   = 2
	codeMask   = 0x1fffff
	partShift  = 21
)

// cell is 21 bits of code point and 2 of part, and a style id (0 is the
// default style).
type cell struct{ bits, style uint32 }

func newCell(c rune, part int, st uint32) cell {
	return cell{bits: uint32(c)&codeMask | uint32(part)<<partShift, style: st}
}

func (c cell) part() int { return int(c.bits >> partShift & 3) }

// styles is Ghostty's RefCountedSet: id 0 is the default style, never
// counted; a style whose count falls to 0 leaves the set and its id is
// free.
type styles struct {
	list   []style
	counts []uint32
	ids    map[style]uint32
	free   []uint32
}

func (t *styles) intern(st style) uint32 {
	if st == defaultStyle {
		return 0
	}
	if id, ok := t.ids[st]; ok {
		return id
	}
	var id uint32
	if n := len(t.free); n > 0 {
		id = t.free[n-1]
		t.free = t.free[:n-1]
		t.list[id] = st
	} else {
		id = uint32(len(t.list))
		t.list = append(t.list, st)
		t.counts = append(t.counts, 0)
	}
	t.ids[st] = id
	return id
}

func (t *styles) retain(id uint32) {
	if id != 0 {
		t.counts[id]++
	}
}

func (t *styles) release(id uint32) {
	if id == 0 {
		return
	}
	t.counts[id]--
	if t.counts[id] == 0 {
		delete(t.ids, t.list[id])
		t.free = append(t.free, id)
	}
}

type cursor struct {
	row, col                                      int
	pending, visible, origin, autowrap, alternate bool
}

type saved struct {
	row, col        int
	pen             style
	origin, pending bool
}

var blank cell

var home = cursor{visible: true, autowrap: true}

type screen struct {
	cells     []cell
	flags     []byte
	ringLines int
	base      int
	first     int
	limit     int
	cur       cursor
	pen       style
	penID     uint32
	top       int
	bottom    int
	saved     *saved
	title     string
	styles    styles
}

func historyPages() int { return max(scrollback/(pageRows*cols*cellBytes), 1) }

func newScreen() *screen {
	active := (rows + pageRows - 1) / pageRows
	limit := historyPages() * pageRows
	s := &screen{
		ringLines: (historyPages() + active + 1) * pageRows,
		limit:     limit,
		cur:       home,
		bottom:    rows - 1,
		styles:    styles{list: []style{defaultStyle}, counts: []uint32{0}, ids: map[style]uint32{}},
	}
	s.cells = make([]cell, 0, (rows+s.ringLines)*cols)
	s.flags = make([]byte, 0, rows+s.ringLines)
	s.grow(rows)
	for range active {
		s.grow(pageRows)
	}
	return s
}

func (s *screen) grow(n int) {
	s.cells = append(s.cells, make([]cell, n*cols)...)
	s.flags = append(s.flags, make([]byte, n)...)
}

func (s *screen) slotRow(n int) int { return rows + n%s.ringLines }

func (s *screen) rowOf(r int) int {
	if s.cur.alternate {
		return r
	}
	return s.slotRow(s.first + r)
}

func (s *screen) set(ri, i int, c cell) {
	old := s.cells[i].style
	if old != c.style {
		s.styles.retain(c.style)
		s.styles.release(old)
		if c.style != 0 {
			s.flags[ri] |= flagStyled
		}
	}
	s.cells[i] = c
}

func (s *screen) clearRow(ri int) {
	row := s.cells[ri*cols : (ri+1)*cols]
	if s.flags[ri]&flagStyled != 0 {
		for i := range row {
			s.styles.release(row[i].style)
			row[i] = blank
		}
	} else {
		clear(row)
	}
	s.flags[ri] = 0
}

func (s *screen) moveRow(from, to int) {
	copy(s.cells[to*cols:(to+1)*cols], s.cells[from*cols:(from+1)*cols])
	s.flags[to] = s.flags[from]
}

func (s *screen) dropRow(ri int) {
	clear(s.cells[ri*cols : (ri+1)*cols])
	s.flags[ri] = 0
}

func (s *screen) print(run []byte) {
	for i, n := 0, len(run); i < n; {
		wrap := s.cur.pending && s.cur.autowrap
		s.cur.pending = false
		if wrap {
			s.wrapLine()
		}
		col := s.cur.col
		k := min(n-i, cols-col)
		ri := s.rowOf(s.cur.row)
		s.split(ri, col, col+k)
		id := s.penID
		dst := s.cells[ri*cols+col : ri*cols+col+k]
		src := run[i : i+k]
		for j := range dst {
			if old := dst[j].style; old != id {
				s.styles.retain(id)
				s.styles.release(old)
			}
			dst[j] = cell{bits: uint32(src[j]), style: id}
		}
		if id != 0 {
			s.flags[ri] |= flagStyled
		}
		i += k
		if col+k < cols {
			s.cur.col = col + k
		} else {
			s.cur.col = cols - 1
			s.cur.pending = s.cur.autowrap
		}
	}
}

func (s *screen) printRune(c rune, width int) {
	wrap := s.cur.pending && s.cur.autowrap
	s.cur.pending = false
	if wrap {
		s.wrapLine()
	}
	if width == 2 && s.cur.col == cols-1 {
		if !s.cur.autowrap {
			return
		}
		s.erase(s.rowOf(s.cur.row), cols-1, cols)
		s.wrapLine()
	}
	ri := s.rowOf(s.cur.row)
	col := s.cur.col
	s.split(ri, col, col+width)
	at := ri*cols + col
	if width == 2 {
		s.set(ri, at, newCell(c, partHead, s.penID))
		s.set(ri, at+1, newCell(0, partTail, s.penID))
	} else {
		s.set(ri, at, newCell(c, partSingle, s.penID))
	}
	if col+width < cols {
		s.cur.col = col + width
	} else {
		s.cur.col = cols - 1
		s.cur.pending = s.cur.autowrap
	}
}

func (s *screen) wrapLine() {
	s.flags[s.rowOf(s.cur.row)] |= flagWrap
	s.cur.col = 0
	s.lineFeed()
}

func (s *screen) split(ri, from, to int) {
	at := ri * cols
	if from > 0 && s.cells[at+from].part() == partTail {
		s.set(ri, at+from-1, blank)
	}
	if to < cols && s.cells[at+to-1].part() == partHead {
		s.set(ri, at+to, blank)
	}
}

func (s *screen) erase(ri, from, to int) {
	if from >= to {
		return
	}
	s.split(ri, from, to)
	at := ri * cols
	for col := from; col < to; col++ {
		s.set(ri, at+col, blank)
	}
}

func (s *screen) repair(ri int) {
	at := ri * cols
	for col := 0; col < cols; {
		p := s.cells[at+col].part()
		if p == partHead && col+1 < cols && s.cells[at+col+1].part() == partTail {
			col += 2
			continue
		}
		if p != partSingle {
			s.set(ri, at+col, blank)
		}
		col++
	}
}

func (s *screen) backspace() {
	s.cur.pending = false
	if s.cur.col > 0 {
		s.cur.col--
	}
}

func (s *screen) tab() {
	s.cur.pending = false
	s.cur.col = min((s.cur.col/8+1)*8, cols-1)
}

func (s *screen) lineFeed() {
	s.cur.pending = false
	if s.cur.row == s.bottom {
		s.scrollUp(1)
	} else if s.cur.row < rows-1 {
		s.cur.row++
	}
}

func (s *screen) carriageReturn() {
	s.cur.pending = false
	s.cur.col = 0
}

func (s *screen) reverseIndex() {
	s.cur.pending = false
	if s.cur.row == s.top {
		s.shiftDown(s.top, s.bottom, 1)
	} else if s.cur.row > 0 {
		s.cur.row--
	}
}

func (s *screen) scrollUp(n int) {
	if s.top == 0 && s.bottom == rows-1 && !s.cur.alternate {
		for range min(n, rows) {
			s.push()
		}
		return
	}
	s.shiftUp(s.top, s.bottom, n)
}

func (s *screen) push() {
	s.first++
	if s.slotRow(s.first+rows-1) >= len(s.flags) {
		s.grow(pageRows)
	}
	if s.first-s.base > s.limit {
		for n := s.base; n < s.base+pageRows; n++ {
			s.clearRow(s.slotRow(n))
		}
		s.base += pageRows
	}
}

func (s *screen) shiftUp(from, to, n int) {
	k := min(n, to-from+1)
	for r := from; r < from+k; r++ {
		s.clearRow(s.rowOf(r))
	}
	for r := from; r < to-k+1; r++ {
		s.moveRow(s.rowOf(r+k), s.rowOf(r))
	}
	for r := to - k + 1; r < to+1; r++ {
		s.dropRow(s.rowOf(r))
	}
}

func (s *screen) shiftDown(from, to, n int) {
	k := min(n, to-from+1)
	for r := to - k + 1; r < to+1; r++ {
		s.clearRow(s.rowOf(r))
	}
	for r := to - k; r >= from; r-- {
		s.moveRow(s.rowOf(r), s.rowOf(r+k))
	}
	for r := from; r < from+k; r++ {
		s.dropRow(s.rowOf(r))
	}
}

func (s *screen) save() {
	s.saved = &saved{row: s.cur.row, col: s.cur.col, pen: s.pen, origin: s.cur.origin, pending: s.cur.pending}
}

func (s *screen) restore() {
	if k := s.saved; k != nil {
		s.cur.row, s.cur.col = k.row, k.col
		s.setPen(k.pen)
		s.cur.origin, s.cur.pending = k.origin, k.pending
		return
	}
	s.cur.row, s.cur.col = 0, 0
	s.setPen(defaultStyle)
	s.cur.origin, s.cur.pending = false, false
}

func (s *screen) reset() {
	s.cur.alternate = false
	for r := 0; r < rows; r++ {
		s.clearRow(s.rowOf(r))
	}
	s.cur = home
	s.setPen(defaultStyle)
	s.top, s.bottom = 0, rows-1
	s.saved = nil
	s.title = ""
}

func (s *screen) up(n int) {
	s.cur.pending = false
	stop := 0
	if s.cur.row >= s.top {
		stop = s.top
	}
	s.cur.row = max(s.cur.row-n, stop)
}

func (s *screen) down(n int) {
	s.cur.pending = false
	stop := rows - 1
	if s.cur.row <= s.bottom {
		stop = s.bottom
	}
	s.cur.row = min(s.cur.row+n, stop)
}

func (s *screen) forward(n int) {
	s.cur.pending = false
	s.cur.col = min(s.cur.col+n, cols-1)
}

func (s *screen) back(n int) {
	s.cur.pending = false
	s.cur.col = max(s.cur.col-n, 0)
}

func (s *screen) moveTo(row, col int) {
	s.row(row)
	s.column(col)
}

func (s *screen) column(col int) {
	s.cur.pending = false
	s.cur.col = min(max(col, 1)-1, cols-1)
}

func (s *screen) row(row int) {
	s.cur.pending = false
	r := max(row, 1) - 1
	if s.cur.origin {
		s.cur.row = min(s.top+r, s.bottom)
	} else {
		s.cur.row = min(r, rows-1)
	}
}

func (s *screen) eraseDisplay(mode int) {
	s.cur.pending = false
	row := s.cur.row
	switch mode {
	case 0:
		s.erase(s.rowOf(row), s.cur.col, cols)
		for r := row + 1; r < rows; r++ {
			s.clearRow(s.rowOf(r))
		}
	case 1:
		for r := 0; r < row; r++ {
			s.clearRow(s.rowOf(r))
		}
		s.erase(s.rowOf(row), 0, s.cur.col+1)
	default:
		for r := 0; r < rows; r++ {
			s.clearRow(s.rowOf(r))
		}
	}
}

func (s *screen) eraseLine(mode int) {
	s.cur.pending = false
	ri := s.rowOf(s.cur.row)
	switch mode {
	case 0:
		s.erase(ri, s.cur.col, cols)
	case 1:
		s.erase(ri, 0, s.cur.col+1)
	default:
		s.erase(ri, 0, cols)
	}
}

func (s *screen) insertLines(n int) {
	row := s.cur.row
	if row < s.top || row > s.bottom {
		return
	}
	s.cur.pending = false
	s.cur.col = 0
	s.shiftDown(row, s.bottom, n)
}

func (s *screen) deleteLines(n int) {
	row := s.cur.row
	if row < s.top || row > s.bottom {
		return
	}
	s.cur.pending = false
	s.cur.col = 0
	s.shiftUp(row, s.bottom, n)
}

func (s *screen) insertBlanks(n int) {
	s.cur.pending = false
	ri := s.rowOf(s.cur.row)
	col := s.cur.col
	k := min(n, cols-col)
	at := ri * cols
	for j := cols - k; j < cols; j++ {
		s.set(ri, at+j, blank)
	}
	copy(s.cells[at+col+k:at+cols], s.cells[at+col:at+cols-k])
	clear(s.cells[at+col : at+col+k])
	s.repair(ri)
}

func (s *screen) deleteChars(n int) {
	s.cur.pending = false
	ri := s.rowOf(s.cur.row)
	col := s.cur.col
	k := min(n, cols-col)
	at := ri * cols
	for j := col; j < col+k; j++ {
		s.set(ri, at+j, blank)
	}
	copy(s.cells[at+col:at+cols-k], s.cells[at+col+k:at+cols])
	clear(s.cells[at+cols-k : at+cols])
	s.repair(ri)
}

func (s *screen) eraseChars(n int) {
	s.cur.pending = false
	s.erase(s.rowOf(s.cur.row), s.cur.col, min(s.cur.col+n, cols))
}

func (s *screen) setRegion(top, bottom int) {
	t := top
	if t == 0 {
		t = 1
	}
	b := bottom
	if b == 0 {
		b = rows
	}
	b = min(b, rows)
	if t >= b {
		return
	}
	s.top, s.bottom = t-1, b-1
	s.moveTo(1, 1)
}

func (s *screen) setPen(pen style) {
	id := s.styles.intern(pen)
	s.styles.retain(id)
	s.styles.release(s.penID)
	s.penID = id
	s.pen = pen
}

func (s *screen) setMode(mode int, on bool) {
	switch mode {
	case 6:
		s.cur.origin = on
		s.moveTo(1, 1)
	case 7:
		s.cur.autowrap = on
	case 25:
		s.cur.visible = on
	default:
		if on && !s.cur.alternate {
			s.save()
			s.cur.alternate = true
			for r := 0; r < rows; r++ {
				s.clearRow(r)
			}
		} else if !on && s.cur.alternate {
			s.cur.alternate = false
			s.restore()
		}
	}
}

func (s *screen) history() int { return s.first - s.base }

func (s *screen) lineRow(l int) int {
	if h := s.history(); l < h {
		return s.slotRow(s.base + l)
	} else {
		return s.rowOf(l - h)
	}
}

// ---- Output ----

func colorText(b []byte, c int32) []byte {
	switch {
	case c == 0:
		return append(b, '-')
	case c <= 256:
		return strconv.AppendInt(b, int64(c-1), 10)
	}
	v := c - rgbBase
	b = append(b, '#')
	b = strconv.AppendInt(b, int64(v>>16), 10)
	b = append(b, '.')
	b = strconv.AppendInt(b, int64(v>>8&255), 10)
	b = append(b, '.')
	return strconv.AppendInt(b, int64(v&255), 10)
}

func styleText(b []byte, st style) []byte {
	if st == defaultStyle {
		return append(b, '-')
	}
	b = colorText(b, st.fg)
	b = append(b, '/')
	b = colorText(b, st.bg)
	b = append(b, '/')
	return strconv.AppendInt(b, int64(st.flags), 10)
}

// rowText appends line l, styled, with its blank right end trimmed.
func (s *screen) rowText(b []byte, l int) []byte {
	start := len(b)
	ri := s.lineRow(l)
	cur := defaultStyle
	for _, c := range s.cells[ri*cols : (ri+1)*cols] {
		if st := s.styles.list[c.style]; st != cur {
			b = append(b, '{')
			b = styleText(b, st)
			b = append(b, '}')
			cur = st
		}
		if c.part() == partTail {
			continue
		}
		if code := rune(c.bits & codeMask); code == 0 {
			b = append(b, ' ')
		} else {
			b = utf8.AppendRune(b, code)
		}
	}
	if s.flags[ri]&flagWrap != 0 {
		b = append(b, '\\')
	}
	end := len(b)
	for end > start && b[end-1] == ' ' {
		end--
	}
	return b[:end]
}

func appendBool(b []byte, v bool) []byte { return strconv.AppendBool(b, v) }

func main() {
	if len(os.Args) > 1 && (len(os.Args) != 2 || os.Args[1] != "replay") {
		os.Stderr.WriteString("usage: control [replay]\n")
		os.Exit(2)
	}
	in, err := io.ReadAll(os.Stdin)
	if err != nil {
		os.Stderr.WriteString("control: " + err.Error() + "\n")
		os.Exit(1)
	}
	s := newScreen()
	p := newParser(s)
	p.feed(in)
	p.finish()

	w := bufio.NewWriterSize(os.Stdout, 1<<16)
	lines := s.history() + rows
	var b []byte
	blanks := 0
	for l := 0; l < lines; l++ {
		b = s.rowText(b[:0], l)
		if len(b) == 0 {
			blanks++
			continue
		}
		for ; blanks > 0; blanks-- {
			w.WriteByte('\n')
		}
		w.Write(b)
		w.WriteByte('\n')
	}
	c := s.cur
	b = append(b[:0], "cursor "...)
	b = strconv.AppendInt(b, int64(c.row), 10)
	b = append(b, ',')
	b = strconv.AppendInt(b, int64(c.col), 10)
	b = append(b, " pending "...)
	b = appendBool(b, c.pending)
	b = append(b, " visible "...)
	b = appendBool(b, c.visible)
	b = append(b, " origin "...)
	b = appendBool(b, c.origin)
	b = append(b, " autowrap "...)
	b = appendBool(b, c.autowrap)
	b = append(b, " alternate "...)
	b = appendBool(b, c.alternate)
	b = append(b, "\npen "...)
	b = styleText(b, s.pen)
	b = append(b, "\ntitle "...)
	b = append(b, s.title...)
	b = append(b, "\nbytes "...)
	b = strconv.AppendInt(b, int64(p.bytes), 10)
	b = append(b, " dropped "...)
	b = strconv.AppendInt(b, int64(p.dropped), 10)
	b = append(b, " bad-text "...)
	b = strconv.AppendInt(b, int64(p.bad), 10)
	b = append(b, " osc-cut "...)
	b = strconv.AppendInt(b, int64(p.oscCut), 10)
	b = append(b, '\n')
	w.Write(b)
	names := make([]string, 0, len(p.notes))
	counts := map[string]int{}
	for n, k := range p.notes {
		name := n.String()
		names = append(names, name)
		counts[name] = k
	}
	sort.Strings(names)
	for _, name := range names {
		w.WriteString("unhandled " + name + " " + strconv.Itoa(counts[name]) + "\n")
	}
	w.Flush()
}
