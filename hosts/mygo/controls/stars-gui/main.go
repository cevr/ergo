// Stars-gui is the MyGo-direct control of examples/stars-gui (corpus
// wave GP1c): the table's model as cmd/ergo/testdata/stars-gui/control
// writes it in hand Go (a column store, ids sorted in place, a filter that
// narrows in place, frames written to one buffer), copied here, and the
// table's window on MyGo's own widgets, its rows in a ui.List. It is what
// the Ergo window binary (cmd/stars-gui) is measured against: the cost of
// a frame, binary size, and the same output.
//
//	stars-gui [-rows H] CATALOG            open the window
//	stars-gui -headless [-rows H] CATALOG  play the script on standard
//	                                       input, a frame of ui.Tester a
//	                                       line, and print what the Ergo
//	                                       binaries print
//
// Headless, a line goes to the model, not through the widgets: the
// output is the model's frame, and the window is laid out and painted
// after each line, scrolled to the table's window.
package main

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"os"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

const (
	colName = iota
	colConstellation
	colMagnitude
	colDistance
	colClass
	colAscension
	colDeclination
	colPlanets
	numColumns
)

var columnNames = [numColumns]string{"name", "constellation", "magnitude", "distance", "class", "ascension", "declination", "planets"}

// table is the catalogue as columns. texts holds the three text columns
// (name, constellation, class), nums the five number columns.
type table struct {
	name, constellation, class []string
	lowerName, lowerConst      []string
	nums                       [numColumns][]int
}

func (t *table) len() int { return len(t.name) }

func isText(c int) bool { return c == colName || c == colConstellation || c == colClass }

func places(c int) int {
	switch c {
	case colMagnitude:
		return 2
	case colDistance, colDeclination:
		return 1
	}
	return 0
}

func (t *table) text(c, id int) string {
	switch c {
	case colName:
		return t.name[id]
	case colConstellation:
		return t.constellation[id]
	case colClass:
		return t.class[id]
	}
	return ""
}

// cellText appends the text of column c of row id to b.
func (t *table) cellText(b []byte, c, id int) []byte {
	if isText(c) {
		return append(b, t.text(c, id)...)
	}
	v := t.nums[c][id]
	switch c {
	case colAscension:
		h, m := v/60, v%60
		b = pad2(b, h)
		b = append(b, 'h')
		b = pad2(b, m)
		return append(b, 'm')
	case colPlanets:
		return strconv.AppendInt(b, int64(v), 10)
	}
	return appendFixed(b, v, places(c))
}

func pad2(b []byte, v int) []byte {
	s := strconv.Itoa(v)
	for i := len(s); i < 2; i++ {
		b = append(b, '0')
	}
	return append(b, s...)
}

func appendFixed(b []byte, v, places int) []byte {
	a := v
	if v < 0 {
		a = -v
		b = append(b, '-')
	}
	scale := 1
	for i := 0; i < places; i++ {
		scale *= 10
	}
	b = strconv.AppendInt(b, int64(a/scale), 10)
	if places == 0 {
		return b
	}
	b = append(b, '.')
	f := strconv.Itoa(a % scale)
	for i := len(f); i < places; i++ {
		b = append(b, '0')
	}
	return append(b, f...)
}

func fixedText(v, places int) string { return string(appendFixed(nil, v, places)) }

// refused is a text a column does not take.
type refused struct {
	column int
	text   string
	reason string
}

func digits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func atoi(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		panic(err)
	}
	return n
}

func parseFixed(text string, places int) (int, bool) {
	neg := strings.HasPrefix(text, "-")
	body := text
	if neg {
		body = text[1:]
	}
	whole, frac := body, ""
	if before, after, ok := strings.Cut(body, "."); ok {
		whole, frac = before, after
		if len(frac) == 0 || len(frac) > places {
			return 0, false
		}
	}
	if len(whole) == 0 || len(whole) > 9 || !digits(whole) || !digits(frac) {
		return 0, false
	}
	scale := 1
	for i := 0; i < places; i++ {
		scale *= 10
	}
	v := atoi(whole) * scale
	if frac != "" {
		s := 1
		for i := 0; i < places-len(frac); i++ {
			s *= 10
		}
		v += atoi(frac) * s
	}
	if neg {
		v = -v
	}
	return v, true
}

const notANumber = "not a number of this column's form"

// parseCell reads text as a cell of column c: the text, or the number.
func parseCell(c int, text string) (string, int, *refused) {
	if isText(c) {
		if strings.TrimSpace(text) == "" {
			return "", 0, &refused{c, text, "a blank value"}
		}
		if strings.ContainsAny(text, "\t\n\r") {
			return "", 0, &refused{c, text, "a tab or a line break"}
		}
		return text, 0, nil
	}
	fixed := func(places, lo, hi int) (string, int, *refused) {
		v, ok := parseFixed(text, places)
		if !ok {
			return "", 0, &refused{c, text, notANumber}
		}
		if v < lo || v > hi {
			return "", 0, &refused{c, text, "out of range " + fixedText(lo, places) + " to " + fixedText(hi, places)}
		}
		return "", v, nil
	}
	switch c {
	case colMagnitude:
		return fixed(2, -3000, 3000)
	case colDistance:
		return fixed(1, 0, 99999999)
	case colDeclination:
		return fixed(1, -900, 900)
	case colAscension:
		if !(len(text) == 6 && text[2] == 'h' && text[5] == 'm' && digits(text[0:2]) && digits(text[3:5])) {
			return "", 0, &refused{c, text, notANumber}
		}
		h, m := atoi(text[0:2]), atoi(text[3:5])
		if h > 23 || m > 59 {
			return "", 0, &refused{c, text, "out of range 00h00m to 23h59m"}
		}
		return "", h*60 + m, nil
	}
	if text == "" || !digits(text) || len(text) > 4 {
		return "", 0, &refused{c, text, notANumber}
	}
	n := atoi(text)
	if n > 999 {
		return "", 0, &refused{c, text, "out of range 0 to 999"}
	}
	return "", n, nil
}

func (t *table) set(id, c int, s string, v int) {
	switch c {
	case colName:
		t.name[id], t.lowerName[id] = s, strings.ToLower(s)
	case colConstellation:
		t.constellation[id], t.lowerConst[id] = s, strings.ToLower(s)
	case colClass:
		t.class[id] = s
	default:
		t.nums[c][id] = v
	}
}

func (t *table) add() int {
	t.name = append(t.name, "")
	t.constellation = append(t.constellation, "")
	t.class = append(t.class, "")
	t.lowerName = append(t.lowerName, "")
	t.lowerConst = append(t.lowerConst, "")
	for c := range t.nums {
		if !isText(c) {
			t.nums[c] = append(t.nums[c], 0)
		}
	}
	return len(t.name) - 1
}

func parseCatalogue(text string) (*table, string) {
	t := &table{}
	n := 0
	for len(text) > 0 {
		n++
		line := text
		if i := strings.IndexByte(text, '\n'); i >= 0 {
			line, text = text[:i], text[i+1:]
		} else {
			text = ""
		}
		cells := strings.Split(line, "\t")
		if len(cells) != numColumns {
			return nil, fmt.Sprintf("line %d: %d cells, want 8", n, len(cells))
		}
		id := t.add()
		for c, cell := range cells {
			s, v, r := parseCell(c, cell)
			if r != nil {
				return nil, fmt.Sprintf("line %d: %s \"%s\": %s", n, columnNames[c], r.text, r.reason)
			}
			t.set(id, c, s, v)
		}
	}
	return t, ""
}

// The modes.
const (
	browsing = iota
	filtering
	editing
)

type state struct {
	t        *table
	view     []int
	key      int
	desc     bool
	filter   string
	top      int
	cursor   int
	height   int
	anchor   int // -1: none
	selected []int

	mode      int
	before    string // filtering: the filter to restore
	editRow   int
	editCol   int
	draft     string
	scratch   []int
	lowerFilt string
}

func (s *state) matches(id int, f string) bool {
	return f == "" || strings.Contains(s.t.lowerName[id], f) || strings.Contains(s.t.lowerConst[id], f)
}

// order is the ids that filter keeps, sorted by the key, into s.view.
func (s *state) order() {
	f := strings.ToLower(s.filter)
	s.view = s.view[:0]
	for id := 0; id < s.t.len(); id++ {
		if s.matches(id, f) {
			s.view = append(s.view, id)
		}
	}
	s.sort()
}

func (s *state) sort() {
	desc := s.desc
	if isText(s.key) {
		col := s.t.name
		switch s.key {
		case colConstellation:
			col = s.t.constellation
		case colClass:
			col = s.t.class
		}
		slices.SortFunc(s.view, func(a, b int) int {
			c := strings.Compare(col[a], col[b])
			if desc {
				c = -c
			}
			if c != 0 {
				return c
			}
			return a - b
		})
		return
	}
	col := s.t.nums[s.key]
	slices.SortFunc(s.view, func(a, b int) int {
		x, y := col[a], col[b]
		if x != y {
			if (x < y) != desc {
				return -1
			}
			return 1
		}
		return a - b
	})
}

func (s *state) settle() {
	n := len(s.view)
	last := max(n-1, 0)
	s.cursor = min(max(s.cursor, 0), last)
	if s.cursor < s.top {
		s.top = s.cursor
	}
	if s.cursor >= s.top+s.height {
		s.top = s.cursor - s.height + 1
	}
	room := max(n-s.height, 0)
	s.top = min(max(s.top, 0), room)
}

func (s *state) follow(id int) {
	if i := slices.Index(s.view, id); i >= 0 {
		s.cursor = i
	}
}

func dropLast(text string) string {
	last := 0
	for i := range text {
		last = i
	}
	if utf8.ValidString(text[last:]) {
		return text[:last]
	}
	return text[:len(text)-1]
}

func columnOf(name string) int {
	for c, n := range columnNames {
		if n == name {
			return c
		}
	}
	return -1
}

const wantCommand = "a command: show down up pagedown pageup home end sort filter type backspace enter escape mark extend edit"
const wantColumn = "a column: name constellation magnitude distance class ascension declination planets"

type player struct {
	s   state
	out *bufio.Writer
	buf []byte

	// The window's: the list's place, the field's text, the row line's
	// buffer.
	list   ui.ListState
	field  string
	rowBuf []byte
}

func (p *player) bad(line, want string) {
	p.out.WriteString("? \"" + line + "\": want " + want + "\n")
}

func (p *player) line(line string) {
	word, rest, _ := strings.Cut(line, " ")
	bare := func() bool {
		if rest != "" {
			p.bad(line, "no argument")
			return false
		}
		return true
	}
	s := &p.s
	switch word {
	case "show":
		if bare() {
			p.frame()
		}
	case "down", "up":
		n, err := strconv.Atoi(rest)
		if err != nil || n < 0 || n > 1000000000 {
			p.bad(line, "a count of rows")
			return
		}
		if word == "up" {
			n = -n
		}
		if s.mode == browsing {
			s.cursor += n
			s.settle()
		}
	case "pagedown", "pageup":
		if bare() && s.mode == browsing {
			by := s.height
			if word == "pageup" {
				by = -by
			}
			s.cursor += by
			s.top += by
			s.settle()
		}
	case "home", "end":
		if bare() && s.mode == browsing {
			if word == "home" {
				s.cursor = 0
			} else {
				s.cursor = len(s.view) - 1
			}
			s.settle()
		}
	case "sort":
		c := columnOf(rest)
		if c < 0 {
			p.bad(line, wantColumn)
			return
		}
		if s.mode != browsing {
			return
		}
		if c == s.key {
			s.desc = !s.desc
		} else {
			s.key, s.desc = c, false
		}
		id := -1
		if len(s.view) > 0 {
			id = s.view[s.cursor]
		}
		s.sort()
		s.follow(id)
		s.settle()
	case "filter":
		if bare() && s.mode == browsing {
			s.mode, s.before = filtering, s.filter
		}
	case "type":
		switch s.mode {
		case filtering:
			next := s.filter + rest
			if strings.HasPrefix(next, s.filter) && utf8.ValidString(s.filter) && utf8.ValidString(next) {
				f := strings.ToLower(next)
				kept := s.view[:0]
				for _, id := range s.view {
					if s.matches(id, f) {
						kept = append(kept, id)
					}
				}
				s.view = kept
				s.filter = next
			} else {
				s.filter = next
				s.order()
			}
			s.cursor, s.top = 0, 0
			s.settle()
		case editing:
			s.draft += rest
		}
	case "backspace":
		if !bare() {
			return
		}
		switch s.mode {
		case filtering:
			if s.filter != "" {
				s.filter = dropLast(s.filter)
				s.order()
				s.cursor, s.top = 0, 0
				s.settle()
			}
		case editing:
			if s.draft != "" {
				s.draft = dropLast(s.draft)
			}
		}
	case "enter":
		if !bare() {
			return
		}
		switch s.mode {
		case filtering:
			s.mode = browsing
		case editing:
			text, v, r := parseCell(s.editCol, s.draft)
			if r != nil {
				p.out.WriteString("refused " + columnNames[r.column] + " \"" + r.text + "\": " + r.reason + "\n")
				return
			}
			s.t.set(s.editRow, s.editCol, text, v)
			s.order()
			s.follow(s.editRow)
			s.settle()
			s.mode = browsing
		}
	case "escape":
		if !bare() {
			return
		}
		switch s.mode {
		case browsing:
			s.anchor, s.selected = -1, s.selected[:0]
		case filtering:
			s.filter = s.before
			s.order()
			s.cursor, s.top = 0, 0
			s.settle()
			s.mode = browsing
		case editing:
			s.mode = browsing
		}
	case "mark":
		if bare() && s.mode == browsing && len(s.view) > 0 {
			s.anchor = s.view[s.cursor]
		}
	case "extend":
		if !bare() || s.mode != browsing || s.anchor < 0 {
			return
		}
		from := slices.Index(s.view, s.anchor)
		if from < 0 {
			return
		}
		lo, hi := min(from, s.cursor), max(from, s.cursor)
		s.selected = append(s.selected[:0], s.view[lo:hi+1]...)
		slices.Sort(s.selected)
	case "edit":
		c := columnOf(rest)
		if c < 0 {
			p.bad(line, wantColumn)
			return
		}
		if s.mode == browsing && len(s.view) > 0 {
			id := s.view[s.cursor]
			s.mode, s.editRow, s.editCol = editing, id, c
			s.draft = string(s.t.cellText(nil, c, id))
		}
	default:
		p.bad(line, wantCommand)
	}
}

func (p *player) frame() {
	s := &p.s
	b := p.buf[:0]
	switch s.mode {
	case browsing:
		b = append(b, "browsing"...)
	case filtering:
		b = append(b, "filtering"...)
	case editing:
		b = append(b, "editing"...)
	}
	b = append(b, " sort="...)
	b = append(b, columnNames[s.key]...)
	if s.desc {
		b = append(b, " desc"...)
	} else {
		b = append(b, " asc"...)
	}
	b = append(b, " filter=\""...)
	b = append(b, s.filter...)
	b = append(b, "\" shown="...)
	b = strconv.AppendInt(b, int64(len(s.view)), 10)
	b = append(b, '/')
	b = strconv.AppendInt(b, int64(s.t.len()), 10)
	b = append(b, " top="...)
	b = strconv.AppendInt(b, int64(s.top), 10)
	b = append(b, " cursor="...)
	b = strconv.AppendInt(b, int64(s.cursor), 10)
	b = append(b, " selected="...)
	b = strconv.AppendInt(b, int64(len(s.selected)), 10)
	if s.mode == editing {
		b = append(b, " edit="...)
		b = append(b, columnNames[s.editCol]...)
		b = append(b, " row="...)
		b = strconv.AppendInt(b, int64(s.editRow), 10)
		b = append(b, " draft=\""...)
		b = append(b, s.draft...)
		b = append(b, '"')
	}
	b = append(b, '\n')
	end := min(s.top+s.height, len(s.view))
	for at := s.top; at < end; at++ {
		id := s.view[at]
		if at == s.cursor {
			b = append(b, '>')
		} else {
			b = append(b, ' ')
		}
		if _, ok := slices.BinarySearch(s.selected, id); ok {
			b = append(b, '*')
		} else {
			b = append(b, ' ')
		}
		b = append(b, ' ')
		b = strconv.AppendInt(b, int64(id), 10)
		for c := 0; c < numColumns; c++ {
			b = append(b, '\t')
			b = s.t.cellText(b, c, id)
		}
		b = append(b, '\n')
	}
	p.out.Write(b)
	p.buf = b
}

// appendRow appends the line of the view's row at position at, as frame
// writes it, without its line break.
func (p *player) appendRow(b []byte, at int) []byte {
	s := &p.s
	id := s.view[at]
	if at == s.cursor {
		b = append(b, '>')
	} else {
		b = append(b, ' ')
	}
	if _, ok := slices.BinarySearch(s.selected, id); ok {
		b = append(b, '*')
	} else {
		b = append(b, ' ')
	}
	b = append(b, ' ')
	b = strconv.AppendInt(b, int64(id), 10)
	for c := 0; c < numColumns; c++ {
		b = append(b, '\t')
		b = s.t.cellText(b, c, id)
	}
	return b
}

// status is the status line, the first line of a frame.
func (p *player) status() string {
	keep := p.out
	var sb strings.Builder
	p.out = bufio.NewWriter(&sb)
	height := p.s.height
	p.s.height = 0
	p.frame()
	p.out.Flush()
	p.s.height = height
	p.out = keep
	return strings.TrimSuffix(sb.String(), "\n")
}

// view is examples/stars-gui's window (its screen package) on MyGo's own
// widgets: a button a column that sorts by it and one that edits its
// cell, the filter, mark and extend buttons, the status line, the text
// field, and the view's rows in a virtual list (ui.List), one text a row.
func (p *player) view(c *ui.Context) {
	ui.Column(c).Children(func() {
		ui.Row(c).Children(func() {
			for col := range numColumns {
				if ui.Button(c, columnNames[col]).Label("sort " + columnNames[col]).Clicked() {
					p.line("sort " + columnNames[col])
				}
			}
		})
		ui.Row(c).Children(func() {
			for col := range numColumns {
				if ui.Button(c, "edit").Label("edit " + columnNames[col]).Clicked() {
					p.line("edit " + columnNames[col])
				}
			}
			for _, w := range []string{"filter", "mark", "extend"} {
				if ui.Button(c, w).Label(w).Clicked() {
					p.line(w)
				}
			}
		})
		ui.Text(c, p.status())
		before := p.typed()
		p.field = before
		ui.TextInput(c, &p.field).Placeholder("text").Label("text").Width(240)
		// The field reports its whole text; the table takes letters and
		// backspaces, as the Ergo app's Retext does.
		if p.field != before {
			if strings.HasPrefix(p.field, before) {
				p.line("type " + p.field[len(before):])
			} else if strings.HasPrefix(before, p.field) {
				for range utf8.RuneCountInString(before[len(p.field):]) {
					p.line("backspace")
				}
			}
		}
		ui.List(c, &p.list, len(p.s.view), func(at int) {
			p.rowBuf = p.appendRow(p.rowBuf[:0], at)
			ui.Text(c, string(p.rowBuf))
		}).Height(560)
	})
}

// typed is what the text field shows: the filter, or the draft.
func (p *player) typed() string {
	switch p.s.mode {
	case filtering:
		return p.s.filter
	case editing:
		return p.s.draft
	}
	return ""
}

// readCatalogue reads and parses the catalogue named name, or exits.
func readCatalogue(name string) *table {
	data, err := os.ReadFile(name)
	if err != nil {
		reason := "no such file"
		if !os.IsNotExist(err) {
			reason = err.Error()
		}
		fmt.Fprintf(os.Stderr, "stars-gui: %s: %s\n", name, reason)
		os.Exit(1)
	}
	t, bad := parseCatalogue(string(data))
	if bad != "" {
		fmt.Fprintf(os.Stderr, "stars-gui: %s: %s\n", name, bad)
		os.Exit(1)
	}
	return t
}

const usage = "usage: stars-gui [-headless] [-rows H] CATALOG"

func main() {
	headless, height, name := false, 10, ""
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "-headless":
			headless = true
		case args[i] == "-rows" && i+1 < len(args):
			h, err := strconv.Atoi(args[i+1])
			if err != nil || h < 1 {
				fmt.Fprintln(os.Stderr, usage)
				os.Exit(2)
			}
			height, i = h, i+1
		case name == "":
			name = args[i]
		default:
			fmt.Fprintln(os.Stderr, usage)
			os.Exit(2)
		}
	}
	if name == "" {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	t := readCatalogue(name)
	out := bufio.NewWriterSize(os.Stdout, 1<<16)
	defer out.Flush()
	p := &player{out: out}
	p.s = state{t: t, key: colName, height: min(max(height, 1), 1000), anchor: -1}
	p.s.order()
	if !headless {
		mygo.App.WhenReady(func() {
			mygo.NewWindow(mygo.WindowOptions{Title: "Stars", Width: 1000, Height: 720, Content: ui.View(p.view)})
		})
		if err := mygo.App.Run(); err != nil {
			log.Fatal(err)
		}
		return
	}
	// Headless: each line goes to the model, as the buttons, keys and
	// field would send it, then ui.Tester lays the window out and paints
	// it with the list scrolled to the table's window: the frame a person
	// sees after the line, which is what this control measures.
	tt := ui.NewTester(p.view, 1000, 720)
	in, err := io.ReadAll(os.Stdin)
	if err != nil {
		panic(err)
	}
	text := string(in)
	for len(text) > 0 {
		line := text
		if i := strings.IndexByte(text, '\n'); i >= 0 {
			line, text = text[:i], text[i+1:]
		} else {
			text = ""
		}
		if line == "" || line[0] == '#' {
			continue
		}
		p.line(line)
		if line == "show" {
			p.shows(tt)
		}
		p.list.ScrollTo(p.s.top, ui.Start)
		tt.Frame()
	}
}

// shows writes a line for each row of the table's window that the last
// frame of the window does not show, so the output holds the window to
// the model as well as the model to the Ergo program.
func (p *player) shows(tt *ui.Tester) {
	end := min(p.s.top+p.s.height, len(p.s.view))
	for at := p.s.top; at < end; at++ {
		p.rowBuf = p.appendRow(p.rowBuf[:0], at)
		// Find's box is clipped to the list: empty for a row out of view.
		if r, ok := tt.Find(string(p.rowBuf)); !ok || r.W <= 0 || r.H <= 0 {
			fmt.Fprintf(p.out, "? the window does not show row %d\n", at)
		}
	}
}
