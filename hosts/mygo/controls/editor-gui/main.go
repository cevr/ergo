// Editor-gui is the MyGo-direct control of examples/editor-gui (corpus
// wave GP2d): the editor's model as cmd/ergo/testdata/editor-gui/control
// writes it in hand Go (a piece table over byte slices, an undo log,
// frames written to one buffer), copied here, and the editor's window on
// MyGo's own widgets, its page a text area. It is what the Ergo window
// binary (cmd/editor-gui) is measured against.
//
//	editor-gui FILE             open the window
//	editor-gui -headless FILE   play the script on standard input, a frame
//	                            of ui.Tester a line, and print what the
//	                            Ergo binaries print
//
// Headless, a line goes to the model, not through the widgets: the output
// is the model's, and the window is laid out and painted after each line
// (a move of N is N lines, as a window's keys make it).
package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

type piece struct {
	added       bool
	start, size int
}

type buffer struct {
	original, added             []byte
	originalBreaks, addedBreaks []int
	pieces                      []piece
	size                        int
}

func newBuffer(text []byte) *buffer {
	b := &buffer{original: text, size: len(text)}
	if len(text) > 0 {
		b.pieces = []piece{{start: 0, size: len(text)}}
	}
	b.originalBreaks = appendBreaks(nil, text, 0)
	return b
}

func appendBreaks(into []int, text []byte, base int) []int {
	for i, c := range text {
		if c == '\n' {
			into = append(into, base+i)
		}
	}
	return into
}

func (b *buffer) src(p piece) []byte {
	if p.added {
		return b.added
	}
	return b.original
}

func (b *buffer) breaks(p piece) []int {
	if p.added {
		return b.addedBreaks
	}
	return b.originalBreaks
}

func (b *buffer) slice(from, to int) []byte {
	from, to = max(from, 0), min(to, b.size)
	if from >= to {
		return nil
	}
	out := make([]byte, 0, to-from)
	at := 0
	for _, p := range b.pieces {
		end := at + p.size
		if end > from && at < to {
			s, e := max(from-at, 0), min(to-at, p.size)
			out = append(out, b.src(p)[p.start+s:p.start+e]...)
		}
		if end >= to {
			break
		}
		at = end
	}
	return out
}

func (b *buffer) text() []byte { return b.slice(0, b.size) }

func (b *buffer) insert(at int, text []byte) {
	if len(text) == 0 {
		return
	}
	pos := min(max(at, 0), b.size)
	start := len(b.added)
	b.added = append(b.added, text...)
	b.addedBreaks = appendBreaks(b.addedBreaks, text, start)
	offset := 0
	for i, p := range b.pieces {
		end := offset + p.size
		if pos == end && p.added && p.start+p.size == start {
			b.pieces[i].size += len(text)
			b.size += len(text)
			return
		}
		if pos >= offset && pos < end {
			left := pos - offset
			add := piece{added: true, start: start, size: len(text)}
			rest := piece{added: p.added, start: p.start + left, size: p.size - left}
			var mid []piece
			if left > 0 {
				mid = []piece{{added: p.added, start: p.start, size: left}, add, rest}
			} else {
				mid = []piece{add, rest}
			}
			b.pieces = append(b.pieces[:i], append(mid, b.pieces[i+1:]...)...)
			b.size += len(text)
			return
		}
		offset = end
	}
	b.pieces = append(b.pieces, piece{added: true, start: start, size: len(text)})
	b.size += len(text)
}

func (b *buffer) delete(at, n int) {
	lo := min(max(at, 0), b.size)
	hi := min(max(at+min(max(n, 0), b.size), lo), b.size)
	if lo == hi {
		return
	}
	out := b.pieces[:0:0]
	offset := 0
	for _, p := range b.pieces {
		end := offset + p.size
		if end <= lo || offset >= hi {
			out = append(out, p)
		} else {
			if offset < lo {
				out = append(out, piece{added: p.added, start: p.start, size: lo - offset})
			}
			if end > hi {
				out = append(out, piece{added: p.added, start: p.start + (hi - offset), size: end - hi})
			}
		}
		offset = end
	}
	b.pieces = out
	b.size -= hi - lo
}

// lineOf is the line of byte at, from 1.
func (b *buffer) lineOf(at int) int {
	n, offset := 1, 0
	for _, p := range b.pieces {
		if offset >= at {
			break
		}
		nl := b.breaks(p)
		end := p.start + min(at-offset, p.size)
		n += sort.SearchInts(nl, end) - sort.SearchInts(nl, p.start)
		offset += p.size
	}
	return n
}

func (b *buffer) lines() int { return b.lineOf(b.size) }

func (b *buffer) lineStart(n int) int {
	if n <= 1 {
		return 0
	}
	seen, offset := 0, 0
	for _, p := range b.pieces {
		nl := b.breaks(p)
		first := sort.SearchInts(nl, p.start)
		count := sort.SearchInts(nl, p.start+p.size) - first
		if seen+count >= n-1 {
			return offset + nl[first+(n-1-seen)-1] - p.start + 1
		}
		seen += count
		offset += p.size
	}
	return b.size
}

func (b *buffer) lineEnd(n int) int {
	if n >= b.lines() {
		return b.size
	}
	return b.lineStart(max(n, 1)+1) - 1
}

type edit struct {
	insert bool
	at     int
	text   []byte
}

type change struct {
	edits         []edit
	before, after int
}

type mode int

const (
	editing mode = iota
	selecting
	searching
)

type editor struct {
	buf        *buffer
	caret      int
	undo, redo []change
	dirty      bool
	mode       mode
	anchor     int
	query      string
	origin     int
	clip       string
	name       string
	out        *bufio.Writer
	scratch    []byte
}

func (e *editor) apply(x edit) {
	if x.insert {
		e.buf.insert(x.at, x.text)
	} else {
		e.buf.delete(x.at, len(x.text))
	}
}

func (e *editor) make(edits []edit, after int) {
	if len(edits) == 0 {
		return
	}
	for _, x := range edits {
		e.apply(x)
	}
	e.undo = append(e.undo, change{edits: edits, before: e.caret, after: after})
	e.redo = e.redo[:0]
	e.caret = after
	e.dirty = true
}

func (e *editor) erase(from, to int) {
	if from >= to {
		return
	}
	e.make([]edit{{at: from, text: e.buf.slice(from, to)}}, from)
}

func (e *editor) undoOne() {
	c := e.undo[len(e.undo)-1]
	for i := len(c.edits) - 1; i >= 0; i-- {
		x := c.edits[i]
		e.apply(edit{insert: !x.insert, at: x.at, text: x.text})
	}
	e.undo = e.undo[:len(e.undo)-1]
	e.redo = append(e.redo, c)
	e.caret = c.before
	e.dirty = true
}

func (e *editor) redoOne() {
	c := e.redo[len(e.redo)-1]
	for _, x := range c.edits {
		e.apply(x)
	}
	e.redo = e.redo[:len(e.redo)-1]
	e.undo = append(e.undo, c)
	e.caret = c.after
	e.dirty = true
}

func (e *editor) runeBefore(at int) int {
	if at <= 0 {
		return 0
	}
	_, s := utf8.DecodeLastRune(e.buf.slice(at-4, at))
	return s
}

func (e *editor) runeAfter(at int) int {
	if at >= e.buf.size {
		return 0
	}
	_, s := utf8.DecodeRune(e.buf.slice(at, at+4))
	return s
}

func (e *editor) lineStartAt(at int) int { return e.buf.lineStart(e.buf.lineOf(at)) }
func (e *editor) lineEndAt(at int) int   { return e.buf.lineEnd(e.buf.lineOf(at)) }
func (e *editor) column(at int) int      { return utf8.RuneCount(e.buf.slice(e.lineStartAt(at), at)) }

func (e *editor) forward(from, col, limit int) int {
	line := e.buf.slice(from, limit)
	at := 0
	for i := 0; i < col && at < len(line); i++ {
		_, s := utf8.DecodeRune(line[at:])
		at += s
	}
	return from + at
}

func (e *editor) toLine(n int) int {
	line := min(max(n, 1), e.buf.lines())
	return e.forward(e.buf.lineStart(line), e.column(e.caret), e.buf.lineEnd(line))
}

// move is the caret after a move command, or -1 for a command that is no
// move.
func (e *editor) move(word string, n int) int {
	switch word {
	case "left":
		at := e.caret
		for i := 0; i < n; i++ {
			s := e.runeBefore(at)
			if s == 0 {
				break
			}
			at -= s
		}
		return at
	case "right":
		at := e.caret
		for i := 0; i < n; i++ {
			s := e.runeAfter(at)
			if s == 0 {
				break
			}
			at += s
		}
		return at
	case "up":
		return e.toLine(e.buf.lineOf(e.caret) - min(max(n, 0), e.buf.lines()))
	case "down":
		return e.toLine(e.buf.lineOf(e.caret) + min(max(n, 0), e.buf.lines()))
	case "home":
		return e.lineStartAt(e.caret)
	case "end":
		return e.lineEndAt(e.caret)
	case "top":
		return 0
	case "bottom":
		return e.buf.size
	}
	return -1
}

func firstFrom(text []byte, query string, from int) int {
	if query == "" {
		return from
	}
	at := min(from, len(text))
	if i := bytes.Index(text[at:], []byte(query)); i >= 0 {
		return at + i
	}
	if j := bytes.Index(text, []byte(query)); j >= 0 {
		return j
	}
	return from
}

func matches(text []byte, query string) []int {
	var out []int
	if query == "" {
		return out
	}
	q := []byte(query)
	at := 0
	for {
		i := bytes.Index(text[at:], q)
		if i < 0 {
			return out
		}
		out = append(out, at+i)
		at += i + len(q)
	}
}

var bare = []string{"show", "enter", "back", "del", "home", "end", "top", "bottom", "select", "find", "next", "undo", "redo", "escape", "save", "copy", "cut", "paste"}

// pointAt is the offset of column col on row row of the window (the
// lines frame writes, from 1; a row past the last is the last).
func (e *editor) pointAt(row, col int) int {
	at, lines := e.buf.lineOf(e.caret), e.buf.lines()
	first, last := max(at-2, 1), min(at+2, lines)
	n := min(max(first+row-1, first), last)
	return e.forward(e.buf.lineStart(n), col, e.buf.lineEnd(n))
}

func count(s string) (int, bool) {
	v, err := strconv.Atoi(s)
	return v, err == nil && v >= 0 && v <= 1000000
}

func (e *editor) bad(line, want string) {
	fmt.Fprintf(e.out, "? \"%s\": want %s\n", line, want)
}

func (e *editor) line(line string) {
	if line == "" || strings.HasPrefix(line, "#") {
		return
	}
	word, rest, _ := strings.Cut(line, " ")
	n := 0
	switch word {
	case "type", "replace":
	case "left", "right", "up", "down":
		v, ok := count(rest)
		if !ok {
			e.bad(line, "a count")
			return
		}
		n = v
	case "click":
		r, c, _ := strings.Cut(rest, " ")
		row, ok := count(r)
		col, ok2 := count(c)
		if !ok || !ok2 {
			e.bad(line, "a count")
			return
		}
		if e.mode != searching {
			e.caret = e.pointAt(row, col)
		}
		return
	default:
		known := false
		for _, w := range bare {
			known = known || w == word
		}
		if !known {
			e.bad(line, "a command: "+strings.Join(bare, " ")+" type replace left right up down click")
			return
		}
		if rest != "" {
			e.bad(line, "no argument")
			return
		}
	}
	switch word {
	case "show":
		e.frame()
		return
	case "enter":
		word, rest = "type", "\n"
	case "paste":
		word, rest = "type", e.clip
	}
	switch e.mode {
	case editing:
		e.editing(word, rest, n)
	case selecting:
		e.selecting(word, rest, n)
	case searching:
		e.searching(word, rest)
	}
}

func (e *editor) editing(word, rest string, n int) {
	switch word {
	case "type":
		if rest != "" {
			e.make([]edit{{insert: true, at: e.caret, text: []byte(rest)}}, e.caret+len(rest))
		}
	case "back":
		if s := e.runeBefore(e.caret); s > 0 {
			e.erase(e.caret-s, e.caret)
		}
	case "del":
		if s := e.runeAfter(e.caret); s > 0 {
			e.erase(e.caret, e.caret+s)
		}
	case "select":
		e.mode, e.anchor = selecting, e.caret
	case "find":
		e.mode, e.query, e.origin = searching, "", e.caret
	case "undo":
		if len(e.undo) > 0 {
			e.undoOne()
		}
	case "redo":
		if len(e.redo) > 0 {
			e.redoOne()
		}
	case "save":
		e.save()
	default:
		if at := e.move(word, n); at >= 0 {
			e.caret = at
		}
	}
}

func (e *editor) selecting(word, rest string, n int) {
	lo, hi := min(e.anchor, e.caret), max(e.anchor, e.caret)
	switch word {
	case "type":
		if rest == "" {
			return
		}
		var edits []edit
		if lo < hi {
			edits = append(edits, edit{at: lo, text: e.buf.slice(lo, hi)})
		}
		edits = append(edits, edit{insert: true, at: lo, text: []byte(rest)})
		e.make(edits, lo+len(rest))
		e.mode = editing
	case "back", "del":
		e.erase(lo, hi)
		e.mode = editing
	case "copy":
		e.clip = string(e.buf.slice(lo, hi))
	case "cut":
		e.clip = string(e.buf.slice(lo, hi))
		e.erase(lo, hi)
		e.mode = editing
	case "escape":
		e.mode = editing
	default:
		if at := e.move(word, n); at >= 0 {
			e.caret = at
		}
	}
}

func (e *editor) searching(word, rest string) {
	switch word {
	case "type":
		e.query += rest
		e.caret = firstFrom(e.buf.text(), e.query, e.origin)
	case "back":
		if e.query == "" {
			return
		}
		_, s := utf8.DecodeLastRuneInString(e.query)
		e.query = e.query[:len(e.query)-s]
		e.caret = firstFrom(e.buf.text(), e.query, e.origin)
	case "next":
		text := e.buf.text()
		if e.query == "" || !bytes.Contains(text, []byte(e.query)) {
			return
		}
		e.caret = firstFrom(text, e.query, e.caret+1)
	case "replace":
		at := matches(e.buf.text(), e.query)
		if len(at) == 0 {
			return
		}
		edits := make([]edit, 0, 2*len(at))
		for i := len(at) - 1; i >= 0; i-- {
			edits = append(edits, edit{at: at[i], text: []byte(e.query)}, edit{insert: true, at: at[i], text: []byte(rest)})
		}
		e.make(edits, at[0])
		e.mode = editing
	case "escape":
		e.caret = e.origin
		e.mode = editing
	case "find":
		e.mode = editing
	}
}

func (e *editor) save() {
	text := e.buf.text()
	dir := filepath.Dir(e.name)
	tmp, err := os.CreateTemp(dir, ".editor-*")
	if err == nil {
		_, err = tmp.Write(text)
		if cerr := tmp.Close(); err == nil {
			err = cerr
		}
		if err == nil {
			err = os.Rename(tmp.Name(), e.name)
		}
		if err != nil {
			os.Remove(tmp.Name())
		}
	}
	if err != nil {
		reason := err.Error()
		switch {
		case errors.Is(err, fs.ErrNotExist):
			reason = "no such directory"
		case errors.Is(err, fs.ErrPermission):
			reason = "permission denied"
		}
		fmt.Fprintf(e.out, "cannot save %s: %s\n", e.name, reason)
		return
	}
	fmt.Fprintf(e.out, "saved %s (%d bytes)\n", e.name, len(text))
	e.dirty = false
}

func (e *editor) frame() {
	w := e.out
	names := [...]string{"editing", "selecting", "searching"}
	dirty := "no"
	if e.dirty {
		dirty = "yes"
	}
	at := e.buf.lineOf(e.caret)
	fmt.Fprintf(w, "%s line=%d col=%d caret=%d len=%d undo=%d redo=%d dirty=%s",
		names[e.mode], at, e.column(e.caret), e.caret, e.buf.size, len(e.undo), len(e.redo), dirty)
	switch e.mode {
	case selecting:
		fmt.Fprintf(w, " anchor=%d", e.anchor)
	case searching:
		fmt.Fprintf(w, " find=\"%s\" matches=%d", e.query, len(matches(e.buf.text(), e.query)))
	}
	w.WriteByte('\n')
	lines := e.buf.lines()
	for n := max(at-2, 1); n <= min(at+2, lines); n++ {
		if n == at {
			w.WriteByte('>')
		} else {
			w.WriteByte(' ')
		}
		w.WriteString(strconv.Itoa(n))
		w.WriteByte('|')
		e.scratch = append(e.scratch[:0], e.buf.slice(e.buf.lineStart(n), e.buf.lineEnd(n))...)
		w.Write(e.scratch)
		w.WriteByte('\n')
	}
}

func firstInvalid(text []byte) int {
	for at := 0; at < len(text); {
		r, s := utf8.DecodeRune(text[at:])
		if r == utf8.RuneError && s == 1 {
			return at
		}
		at += s
	}
	return -1
}

// window is the editor's window on MyGo's widgets, the same as the Ergo
// view's: the toolbar's buttons, the replacement field and its button,
// the text input, the status line and the lines around the caret, and
// the page, the shown lines in a text area.
type window struct {
	e        *editor
	with     string
	typed    string
	page     string
	status   string
	lines    []string
	frameBuf strings.Builder
}

var buttons = []string{"undo", "redo", "save", "select", "find", "next", "copy", "cut", "paste"}

// refresh takes the frame's text (the status line and the lines) and the
// page's bytes from the model, as the Ergo view takes them from its state.
func (w *window) refresh() {
	keep := w.e.out
	w.frameBuf.Reset()
	w.e.out = bufio.NewWriter(&w.frameBuf)
	w.e.frame()
	w.e.out.Flush()
	w.e.out = keep
	all := strings.Split(strings.TrimSuffix(w.frameBuf.String(), "\n"), "\n")
	w.status, w.lines = all[0], all[1:]
	b := w.e.buf
	at := b.lineOf(w.e.caret)
	w.page = string(b.slice(b.lineStart(max(at-2, 1)), b.lineEnd(min(at+2, b.lines()))))
}

func (w *window) view(c *ui.Context) {
	ui.Column(c).Children(func() {
		ui.Row(c).Children(func() {
			for _, name := range buttons {
				if ui.Button(c, name).Label(name).Clicked() {
					w.e.line(name)
					w.refresh()
				}
			}
			ui.TextInput(c, &w.with).Placeholder("with").Label("with").Width(240)
			if ui.Button(c, "replace").Label("replace").Clicked() {
				w.e.line("replace " + w.with)
				w.refresh()
			}
		})
		before := w.typed
		ui.TextInput(c, &w.typed).Placeholder("type").Label("type").Width(240)
		if w.typed != before {
			w.e.line("type " + w.typed)
			w.typed = ""
			w.refresh()
		}
		ui.Text(c, w.status)
		for _, l := range w.lines {
			ui.Text(c, l)
		}
		page := w.page
		ui.TextArea(c, &page).Label("page").Width(640)
	})
}

// shown is a line for the status line when the last frame does not show
// it, so the output holds the window to the model as well.
func (w *window) shown(tt *ui.Tester) {
	if r, ok := tt.Find(w.status); !ok || r.W <= 0 || r.H <= 0 {
		fmt.Fprintf(w.e.out, "? the window does not show the status line\n")
	}
}

// asPlayed is a script line as a window plays it: a move of N is N
// moves of 1 (cmd/ergo's editorSceneAsPlay).
func asPlayed(line string) []string {
	word, rest, _ := strings.Cut(line, " ")
	n, err := strconv.Atoi(rest)
	if (word == "left" || word == "right" || word == "up" || word == "down") && err == nil && n >= 0 && n <= 1000000 && rest == strconv.Itoa(n) {
		out := make([]string, n)
		for i := range out {
			out[i] = word + " 1"
		}
		return out
	}
	return []string{line}
}

const usage = "usage: editor-gui [-headless] FILE"

func main() {
	headless, name := false, ""
	for _, a := range os.Args[1:] {
		switch {
		case a == "-headless":
			headless = true
		case name == "" && !strings.HasPrefix(a, "-"):
			name = a
		default:
			fmt.Fprintln(os.Stderr, usage)
			os.Exit(2)
		}
	}
	if name == "" {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	text, err := os.ReadFile(name)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			fmt.Fprintf(os.Stderr, "editor-gui: %s: %v\n", name, err)
			os.Exit(1)
		}
		text = nil
	}
	if bad := firstInvalid(text); bad >= 0 {
		fmt.Fprintf(os.Stderr, "editor-gui: %s: not UTF-8 text: an invalid byte at offset %d\n", name, bad)
		os.Exit(1)
	}
	out := bufio.NewWriterSize(os.Stdout, 1<<16)
	defer out.Flush()
	w := &window{e: &editor{buf: newBuffer(text), name: name, out: out}}
	w.refresh()
	if !headless {
		mygo.App.WhenReady(func() {
			mygo.NewWindow(mygo.WindowOptions{Title: "Editor", Width: 1100, Height: 720, Content: ui.View(w.view)})
		})
		if err := mygo.App.Run(); err != nil {
			log.Fatal(err)
		}
		return
	}
	// Headless: each line goes to the model, as the buttons, keys and
	// fields would send it, then ui.Tester lays the window out and paints
	// it: the frame a person sees after the line, which this control
	// measures.
	tt := ui.NewTester(w.view, 1100, 720)
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	lines := strings.Split(string(input), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	for _, line := range lines {
		for _, l := range asPlayed(line) {
			w.e.line(l)
			w.refresh()
			if l == "show" {
				w.shown(tt)
			}
			tt.Frame()
		}
	}
}
