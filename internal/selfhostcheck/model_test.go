package selfhostcheck

import (
	"fmt"
	"math/rand/v2"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestSelfhostLayoutModel holds the Ergo formatter's layout state
// (format/layout.ergo, align.ergo) to a small independent model of lines
// and cells. Seeded command lists (Space, Newline, NoBlank, Emit, Write,
// Put, Separator, Indent, SetIndent, EndLine) run through the library's
// Steps, then finish with a limit; the text, or the refusal, must be the
// model's, and a refusal must build no byte (the library reports the bytes
// it built). Cells stay at most 40 runes wide, so the model's column
// blocks are the plain ones: a run of adjacent lines with one indentation
// that all have a cell in the column and another after it, padded to the
// widest cell plus one, an all-empty column taking no width. The long-cell
// section rules are the format property's (TestSelfhostFormatAgrees).
//
// Mutants it alone catches: testdata/mutants.json, killer
// selfhostcheck:TestSelfhostLayoutModel.
//
// It builds Go because the property executes the Ergo layout through its
// exported Go ABI: only a built library runs it.
func TestSelfhostLayoutModel(t *testing.T) {
	needsBuild(t)
	seed := selfhostSeed(t)
	r := rand.New(rand.NewPCG(seed, 0x6c61))
	type run struct {
		cmds  []cmd
		limit int
		want  string
	}
	var runs []run
	for range layoutRuns {
		cmds := genCommands(r)
		for {
			if _, _, plain := modelRender(cmds, 0); plain {
				break
			}
			cmds = genCommands(r)
		}
		full, _ := modelRun(cmds, 0)
		size := len(full)
		limits := []int{0, -1, size, size + 1}
		if size > 0 {
			limits = append(limits, size-1, 1+r.IntN(size))
		}
		for _, limit := range limits {
			text, refused := modelRun(cmds, limit)
			want := fmt.Sprintf("ok %d\n%s", len(text), text)
			if refused {
				want = fmt.Sprintf("limit %d built 0\n", limit)
			}
			runs = append(runs, run{cmds, limit, want})
		}
	}
	cases := make([][]string, len(runs))
	for i, x := range runs {
		cases[i] = []string{encodeCommands(x.cmds), strconv.Itoa(x.limit)}
	}
	exe := buildDriver(t, "formatapi")
	answers := runMode(t, exe, "steps", cases, 1)
	replay := "replay: " + seedVariable + "=" + strconv.FormatUint(seed, 10) + " go test -count=1 -run '^TestSelfhostLayoutModel$' ./internal/selfhostcheck"
	failed := 0
	for i, a := range answers {
		if a[0] == runs[i].want {
			continue
		}
		failed++
		if failed > 3 {
			continue
		}
		t.Errorf("seed %d: run %d, limit %d: the layout differs from the model\ncommands: %s\n  model: %q\n  ergo:  %q\n%s",
			seed, i, runs[i].limit, traceCommands(runs[i].cmds), runs[i].want, a[0], replay)
	}
	if failed > 0 {
		t.Errorf("%d of %d runs differ", failed, len(answers))
	}
}

// layoutRuns is the number of generated command lists per seed.
const layoutRuns = 1500

// cmd is one layout command: its kind (the letter of the Steps encoding)
// and its text or number.
type cmd struct {
	kind byte
	text string
	n    int
}

var cmdTexts = []string{"a", "bc", "-", "=", "/", "*", "x\ny", "é", "日本", "", " ", "long_cell_of_thirty_runes_xxxxx", ":", "\n"}

func genCommands(r *rand.Rand) []cmd {
	var out []cmd
	for range r.IntN(40) {
		var c cmd
		switch r.IntN(12) {
		case 0:
			c = cmd{kind: 's'}
		case 1:
			c = cmd{kind: 'n'}
		case 2:
			c = cmd{kind: 'b'}
		case 3:
			c = cmd{kind: 'e', n: r.IntN(2)}
		case 4, 5:
			// The printer writes, puts and separates after an Emit.
			out = append(out, cmd{kind: 'e', n: r.IntN(2)})
			c = cmd{kind: 'w', text: cmdTexts[r.IntN(len(cmdTexts))]}
		case 6:
			out = append(out, cmd{kind: 'e', n: r.IntN(2)})
			c = cmd{kind: 'p', text: cmdTexts[r.IntN(len(cmdTexts)-2)]}
		case 7, 8:
			out = append(out, cmd{kind: 'e', n: r.IntN(2)})
			c = cmd{kind: '|'}
		case 9:
			c = cmd{kind: 'i', n: 1 - 2*r.IntN(2)}
		case 10:
			c = cmd{kind: 't', n: r.IntN(3)}
		default:
			c = cmd{kind: 'l'}
		}
		out = append(out, c)
	}
	return out
}

func encodeCommands(cs []cmd) string {
	var b strings.Builder
	for _, c := range cs {
		switch c.kind {
		case 'e':
			fmt.Fprintf(&b, "e%d", c.n)
		case 'w', 'p':
			fmt.Fprintf(&b, "%c%d:%s", c.kind, len(c.text), c.text)
		case 'i', 't':
			fmt.Fprintf(&b, "%c%d;", c.kind, c.n)
		default:
			b.WriteByte(c.kind)
		}
	}
	return b.String()
}

func traceCommands(cs []cmd) string {
	var parts []string
	for _, c := range cs {
		switch c.kind {
		case 'w', 'p':
			parts = append(parts, fmt.Sprintf("%c%q", c.kind, c.text))
		case 'e', 'i', 't':
			parts = append(parts, fmt.Sprintf("%c%d", c.kind, c.n))
		default:
			parts = append(parts, string(c.kind))
		}
	}
	return strings.Join(parts, " ")
}

// modelLine is a line of the model: its indentation and its cells.
type modelLine struct {
	indent int
	cells  []string
}

// layoutModel is the model's state: the finished lines, the current
// line's cells (the last one open), and the pending whitespace.
type layoutModel struct {
	lines        []modelLine
	open         bool // the current line has an item
	pendingNL    bool
	pendingSpace bool
	noBlank      bool
	indent       int
}

func (m *layoutModel) cur() *modelLine { return &m.lines[len(m.lines)-1] }

func (m *layoutModel) appendText(s string) {
	l := m.cur()
	l.cells[len(l.cells)-1] += s
}

// close ends the current line, dropping empty cells at its end.
func (m *layoutModel) close() {
	if !m.open {
		return
	}
	l := m.cur()
	for len(l.cells) > 1 && l.cells[len(l.cells)-1] == "" {
		l.cells = l.cells[:len(l.cells)-1]
	}
	m.open = false
}

var mergePairs = []string{"--", "++", "&&", "&^", "||", "<-", "<<", ">>", "==", "!=", "<=", ">=", ":=", "..", "//", "/*", "-=", "+=", "*=", "/=", "%=", "&=", "|=", "^=", "*/"}

func (m *layoutModel) lastByte() byte {
	if !m.open {
		return 0
	}
	l := m.cur()
	for i := len(l.cells) - 1; i >= 0; i-- {
		if c := l.cells[i]; c != "" {
			return c[len(c)-1]
		}
	}
	return 0
}

func (m *layoutModel) apply(c cmd) {
	switch c.kind {
	case 's':
		m.pendingSpace = true
	case 'n':
		m.pendingNL = true
	case 'b':
		m.noBlank = true
	case 'e':
		if m.pendingNL && (m.open || len(m.lines) > 0) {
			m.close()
			if c.n == 1 && !m.noBlank {
				m.lines = append(m.lines, modelLine{})
			}
		}
		m.pendingNL, m.noBlank = false, false
		if !m.open {
			m.open, m.pendingSpace = true, false
			m.lines = append(m.lines, modelLine{indent: m.indent, cells: []string{""}})
		} else if m.pendingSpace {
			m.appendText(" ")
			m.pendingSpace = false
		}
	case 'w':
		if c.text == "" {
			return
		}
		if last := m.lastByte(); last != 0 {
			for _, p := range mergePairs {
				if p[0] == last && p[1] == c.text[0] {
					m.appendText(" ")
					break
				}
			}
		}
		parts := strings.Split(c.text, "\n")
		for i, part := range parts {
			if i > 0 {
				m.close()
				m.open = true
				m.lines = append(m.lines, modelLine{indent: -1, cells: []string{""}})
			}
			m.appendText(part)
		}
	case 'p':
		m.appendText(c.text)
	case '|':
		l := m.cur()
		l.cells[len(l.cells)-1] = strings.TrimRight(l.cells[len(l.cells)-1], " ")
		l.cells = append(l.cells, "")
	case 'i':
		m.indent += c.n
	case 't':
		m.indent = c.n
	case 'l':
		m.close()
	}
}

// modelRun is the text of cs under limit, or a refusal.
func modelRun(cs []cmd, limit int) (string, bool) {
	text, refused, _ := modelRender(cs, limit)
	return text, refused
}

// modelRender is modelRun's text and refusal, and whether every cell that
// is padded is at most alignSmall (40) runes wide, the domain of the
// model's plain column blocks.
func modelRender(cs []cmd, limit int) (string, bool, bool) {
	m := &layoutModel{}
	for _, c := range cs {
		m.apply(c)
	}
	m.close()
	lines := m.lines
	plain := true
	for _, l := range lines {
		for j, c := range l.cells {
			if j < len(l.cells)-1 && utf8.RuneCountInString(c) > 40 {
				plain = false
			}
		}
	}
	width := make([][]int, len(lines))
	for i := range lines {
		width[i] = make([]int, len(lines[i].cells))
	}
	for col := 0; ; col++ {
		any := false
		for i := 0; i < len(lines); {
			if len(lines[i].cells) <= col+1 {
				i++
				continue
			}
			any = true
			j := i + 1
			for j < len(lines) && len(lines[j].cells) > col+1 && lines[j].indent == lines[i].indent {
				j++
			}
			w := 0
			for k := i; k < j; k++ {
				w = max(w, utf8.RuneCountInString(lines[k].cells[col]))
			}
			if w > 0 {
				w++
			}
			for k := i; k < j; k++ {
				width[k][col] = w
			}
			i = j
		}
		if !any {
			break
		}
	}
	var out []string
	for i, l := range lines {
		var b strings.Builder
		if l.indent > 0 {
			b.WriteString(strings.Repeat("\t", l.indent))
		}
		for j, c := range l.cells {
			b.WriteString(c)
			if j < len(l.cells)-1 && width[i][j] > 0 {
				b.WriteString(strings.Repeat(" ", width[i][j]-utf8.RuneCountInString(c)))
			}
		}
		out = append(out, b.String())
	}
	for len(out) > 0 && out[0] == "" {
		out = out[1:]
	}
	text := ""
	for _, l := range out {
		text += l + "\n"
	}
	if limit > 0 && len(text) > limit {
		return "", true, plain
	}
	return text, false, plain
}
