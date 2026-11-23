//go:build (linux || darwin) && (amd64 || arm64)

package screen

import (
	"fmt"
	"unicode"
	"unicode/utf8"

	"ergo.local/prototype/hosts/opentui/ot"
)

// Editor is the kind of a one-line field: OpenTUI's EDITOR node over an
// edit buffer and an editor view of its own. Its text is the buffer's
// (SetText replaces it, keeping the caret where the new text has room for
// it); Edit changes it as the editing keys do, at the caret.
const Editor = 3

// The edits of Edit. Offsets count cells; a grapheme (a rune and its
// combining marks) is one unit, a wide one two cells.
const (
	// EditInsert inserts the text at the caret.
	EditInsert = iota
	// EditBackspace deletes the grapheme before the caret, EditDelete the
	// one after it.
	EditBackspace
	EditDelete
	// EditLeft and EditRight move the caret one grapheme, EditHome and
	// EditEnd to the start and the end.
	EditLeft
	EditRight
	EditHome
	EditEnd
	// EditWordLeft moves the caret back over spaces, then over the word
	// before them; EditWordRight forward over spaces, then over the word.
	EditWordLeft
	EditWordRight
	// EditWordBack deletes what EditWordLeft moves over (Ctrl+W).
	EditWordBack
	// EditKillEnd deletes from the caret to the end (Ctrl+K), EditKillStart
	// from the start to the caret (Ctrl+U).
	EditKillEnd
	EditKillStart
	// EditAt puts the caret at cell col of the field (the grapheme
	// boundary at or before it), as a click does.
	EditAt
)

// editor is the native state of an Editor node.
type editor struct {
	buf, view ot.Handle
}

// Spot is where the terminal's cursor is, after the last Present: its
// cell, and whether it shows.
type Spot struct {
	X, Y  int
	Shown bool
}

// newEditor makes the edit buffer and view of node n and binds them to its
// scene node h (owner thread).
func (s *Screen) newEditor(n int, h ot.Handle) error {
	s.calls++
	buf, err := ot.EditBufferCreate(s.ctx, ot.EditBufferOptions{WidthMethod: ot.OT_WIDTH_METHOD_UNICODE, Flags: ot.OT_EDIT_BUFFER_FOLLOW_SESSION_WIDTH})
	if err != nil {
		return fmt.Errorf("screen: Create %d: edit buffer: %w", n, err)
	}
	var view ot.Handle
	steps := []struct {
		what string
		do   func() error
	}{
		{"view", func() (err error) { view, err = ot.EditorViewCreate(s.ctx, buf, 1, 1); return }},
		{"bind", func() error { return ot.SceneSetEditorView(s.ctx, h, view) }},
		// One row high (also where it grows in a column), as wide as its
		// parent in a column; in a row the driver's Grow widens it.
		{"height", func() error {
			return ot.SceneSetStyle(s.ctx, h, ot.OT_STYLE_DIMENSION, ot.OT_STYLE_VALUE_HEIGHT, ot.OT_EDGE_NONE, ot.OT_UNIT_POINT, 1, 0)
		}},
		{"max height", func() error {
			return ot.SceneSetStyle(s.ctx, h, ot.OT_STYLE_VALUE, ot.OT_STYLE_VALUE_MAX_HEIGHT, ot.OT_EDGE_NONE, ot.OT_UNIT_POINT, 1, 0)
		}},
		{"align self", func() error {
			return ot.SceneSetStyle(s.ctx, h, ot.OT_STYLE_ENUM, ot.OT_STYLE_ENUM_ALIGN_SELF, ot.OT_EDGE_NONE, ot.OT_UNIT_UNDEFINED, float32(ot.OT_ALIGN_STRETCH), 0)
		}},
	}
	for _, st := range steps {
		s.calls++
		if err := st.do(); err != nil {
			_ = ot.EditBufferDestroy(s.ctx, buf)
			return fmt.Errorf("screen: Create %d: editor %s: %w", n, st.what, err)
		}
	}
	s.edits[n] = editor{buf: buf, view: view}
	return nil
}

// dropEditor destroys node n's edit buffer and its view (owner thread).
func (s *Screen) dropEditor(n int) error {
	e, ok := s.edits[n]
	if !ok {
		return nil
	}
	delete(s.edits, n)
	s.calls++
	if err := ot.EditBufferDestroy(s.ctx, e.buf); err != nil {
		return fmt.Errorf("screen: Destroy %d: edit buffer: %w", n, err)
	}
	return nil
}

// text is the buffer's text (owner thread).
func (s *Screen) text(e editor) (string, error) {
	s.calls++
	n, err := ot.EditBufferGetText(s.ctx, e.buf, nil)
	if err != nil || n == 0 {
		return "", err
	}
	b := make([]byte, n)
	s.calls++
	if _, err := ot.EditBufferGetText(s.ctx, e.buf, b); err != nil {
		return "", err
	}
	return string(b), nil
}

// caret is the caret's offset in cells (owner thread).
func (s *Screen) caret(e editor) (uint32, error) {
	s.calls++
	i, err := ot.EditBufferGetInfo(s.ctx, e.buf)
	return i.CursorOffset, err
}

// setCaret puts the caret at the grapheme boundary at or before off,
// clamped to the end (owner thread).
func (s *Screen) setCaret(e editor, off uint32) error {
	b, err := s.cellToByte(e, off)
	if err != nil {
		return err
	}
	at, err := s.byteToCell(e, b)
	if err != nil {
		return err
	}
	s.calls++
	return ot.EditBufferSetCursor(s.ctx, e.buf, 0, at)
}

func (s *Screen) cellToByte(e editor, off uint32) (uint32, error) {
	s.calls++
	return ot.EditBufferConvertOffset(s.ctx, e.buf, ot.OT_EDIT_CELL_TO_BYTE, off)
}

func (s *Screen) byteToCell(e editor, b uint32) (uint32, error) {
	s.calls++
	return ot.EditBufferConvertOffset(s.ctx, e.buf, ot.OT_EDIT_BYTE_TO_CELL, b)
}

// replace sets the buffer's text to text unless it is that already; the
// caret keeps its offset where the new text reaches it, else goes to the
// end (owner thread).
func (s *Screen) replace(e editor, text string) error {
	old, err := s.text(e)
	if err != nil || old == text {
		return err
	}
	off, err := s.caret(e)
	if err != nil {
		return err
	}
	s.calls++
	if err := ot.EditBufferSetText(s.ctx, e.buf, []byte(text), 1); err != nil {
		return err
	}
	return s.setCaret(e, off)
}

// word is the byte index a word motion from byte i of text reaches:
// back (or forward) over spaces, then over the characters that are not.
func word(text string, i int, back bool) int {
	space := func(r rune) bool { return unicode.IsSpace(r) }
	if back {
		for i > 0 {
			r, n := utf8.DecodeLastRuneInString(text[:i])
			if !space(r) {
				break
			}
			i -= n
		}
		for i > 0 {
			r, n := utf8.DecodeLastRuneInString(text[:i])
			if space(r) {
				break
			}
			i -= n
		}
		return i
	}
	for i < len(text) {
		r, n := utf8.DecodeRuneInString(text[i:])
		if !space(r) {
			break
		}
		i += n
	}
	for i < len(text) {
		r, n := utf8.DecodeRuneInString(text[i:])
		if space(r) {
			break
		}
		i += n
	}
	return i
}

// edit applies one edit to e (owner thread).
func (s *Screen) edit(e editor, op, col int, text string) error {
	cmd := func(c uint32) error {
		s.calls++
		return ot.EditBufferCommand(s.ctx, e.buf, c, 0)
	}
	// wordAt is the cell offset of a word motion from the caret.
	wordAt := func(back bool) (uint32, uint32, error) {
		t, err := s.text(e)
		if err != nil {
			return 0, 0, err
		}
		off, err := s.caret(e)
		if err != nil {
			return 0, 0, err
		}
		b, err := s.cellToByte(e, off)
		if err != nil {
			return 0, 0, err
		}
		to, err := s.byteToCell(e, uint32(word(t, int(b), back)))
		return off, to, err
	}
	switch op {
	case EditInsert:
		if text == "" {
			return nil
		}
		s.calls++
		return ot.EditBufferInsertText(s.ctx, e.buf, []byte(text))
	case EditBackspace:
		return cmd(ot.OT_EDIT_BACKSPACE)
	case EditDelete:
		return cmd(ot.OT_EDIT_DELETE_FORWARD)
	case EditLeft:
		return cmd(ot.OT_EDIT_MOVE_LEFT)
	case EditRight:
		return cmd(ot.OT_EDIT_MOVE_RIGHT)
	case EditHome:
		s.calls++
		return ot.EditBufferSetCursor(s.ctx, e.buf, 0, 0)
	case EditEnd, EditKillEnd, EditKillStart:
		s.calls++
		end, err := ot.EditBufferGetPosition(s.ctx, e.buf, ot.OT_EDIT_POSITION_END, 0, 0)
		if err != nil {
			return err
		}
		if op == EditEnd {
			s.calls++
			return ot.EditBufferSetCursor(s.ctx, e.buf, 0, end.Col)
		}
		off, err := s.caret(e)
		if err != nil {
			return err
		}
		s.calls++
		if op == EditKillEnd {
			return ot.EditBufferDeleteRange(s.ctx, e.buf, 0, off, 0, end.Col)
		}
		return ot.EditBufferDeleteRange(s.ctx, e.buf, 0, 0, 0, off)
	case EditWordLeft, EditWordRight:
		_, to, err := wordAt(op == EditWordLeft)
		if err != nil {
			return err
		}
		s.calls++
		return ot.EditBufferSetCursor(s.ctx, e.buf, 0, to)
	case EditWordBack:
		off, to, err := wordAt(true)
		if err != nil || to == off {
			return err
		}
		s.calls++
		return ot.EditBufferDeleteRange(s.ctx, e.buf, 0, to, 0, off)
	case EditAt:
		return s.setCaret(e, uint32(max(col, 0)))
	}
	return fmt.Errorf("screen: no edit %d", op)
}

// Edit applies the commands recorded so far, then one edit of the
// Editor n (op, with the cell col of EditAt and the text of EditInsert),
// and is the field's text after it. The next Present shows it.
func (s *Screen) Edit(n, op, col int, text string) (string, error) {
	s.mu.Lock()
	batch := s.batch
	s.batch = nil
	closed := s.done
	s.mu.Unlock()
	if closed {
		return "", fail(CallClosed, n, ErrClosed)
	}
	var out string
	var err error
	s.do(func() {
		if err = s.applyAll(batch); err != nil {
			return
		}
		if err = check(CallEdit, n); err != nil {
			return
		}
		e, ok := s.edits[n]
		if !ok {
			err = fmt.Errorf("screen: Edit %d: no field", n)
			return
		}
		if err = s.edit(e, op, col, text); err != nil {
			err = fmt.Errorf("screen: Edit %d (%d): %w", n, op, err)
			return
		}
		out, err = s.text(e)
	})
	return out, wrap(CallEdit, n, err)
}

// SetPlaceholder sets the dim text an empty Editor n shows.
func (s *Screen) SetPlaceholder(n int, text string) {
	s.record(command{op: opPlaceholder, node: n, text: text})
}

// placeholder applies SetPlaceholder (owner thread).
func (s *Screen) placeholder(n int, text string) error {
	e, ok := s.edits[n]
	if !ok {
		return fmt.Errorf("screen: SetPlaceholder %d: no field", n)
	}
	var chunks []ot.StyledTextChunk
	if text != "" {
		chunks = []ot.StyledTextChunk{{ByteCount: uint32(len(text)), Foreground: [4]uint16{128, 128, 128, 255}, Attributes: uint32(Dim)}}
	}
	s.calls++
	return ot.EditorViewSetPlaceholder(s.ctx, e.view, []byte(text), chunks)
}

// editorLook sets the colors and attributes of an Editor's text (owner
// thread).
func (s *Screen) editorLook(n int, fg, bg uint32, attr int) error {
	e, ok := s.edits[n]
	if !ok {
		return fmt.Errorf("screen: SetLook %d: no field", n)
	}
	flags := uint32(ot.OT_EDITOR_STYLE_FOREGROUND | ot.OT_EDITOR_STYLE_ATTRIBUTES)
	if bg&0xff != 0 {
		flags |= ot.OT_EDITOR_STYLE_BACKGROUND
	}
	s.calls++
	return ot.EditBufferSetDefaults(s.ctx, e.buf, 7, ot.EditorStyle{Flags: flags, Attributes: uint32(attr), Foreground: color(fg), Background: color(bg)})
}

// Caret is the caret's offset in cells in Editor n, -1 for no field.
func (s *Screen) Caret(n int) int {
	c := -1
	s.do(func() {
		if e, ok := s.edits[n]; ok {
			if off, err := s.caret(e); err == nil {
				c = int(off)
			}
		}
	})
	return c
}

// Cursor is where the terminal's cursor is after the last Present.
func (s *Screen) Cursor() Spot {
	var c Spot
	s.do(func() {
		s.calls++
		st, err := ot.SceneGetCursorState(s.ctx, s.session)
		if err != nil {
			panic(err)
		}
		// The state's cell is 1-based, as the terminal's sequence is.
		c = Spot{X: int(st.X) - 1, Y: int(st.Y) - 1, Shown: st.Visible == 1}
	})
	return c
}
