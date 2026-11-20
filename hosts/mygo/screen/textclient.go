package screen

import (
	"unicode/utf16"

	"github.com/egoist/mygo/drawing/platform"
)

// The focused field's document as the input methods see it (MB5): its
// text with any composition in it, the selection and the marked range,
// in UTF-16 code units as the native protocols count them. The input
// methods call the client on the main thread and read the document back
// at once, so the window answers from this copy and never waits for the
// program's task: a committed edit queues an Edited input carrying the
// committed text, which the program makes its field's value; a
// composition queues nothing, so the program never sees half a
// character. Each Edited input is numbered (Input.Edit), and a paint's
// Editing names the last one the program took: while edits it has not
// taken are queued the copy stays as it is (fast typing while a pass
// runs), and once it took them all, a value that is not the copy's
// committed text (the program refused or rewrote it) replaces the copy.
type document struct {
	text   string
	sel    platform.TextSelection
	mark   platform.TextRange
	marked bool
	// sent is the number of the last Edited input queued.
	sent int
	// known is the committed text the program was last told or gave.
	known string
	caret platform.RectF
}

// textClient is the window's platform.TextInputClient.
type textClient struct{ w *Window }

var _ platform.TextInputClient = textClient{}

// units is the length of s in UTF-16 code units.
func units(s string) int { return platform.UTF16Len(s) }

// at is the byte offset of UTF-16 offset i of s, rounded down to a rune.
func at(s string, i int) int { return platform.UTF16ByteOffset(s, i) }

// clamp is r ordered and inside a text of n units.
func clamp(r platform.TextRange, n int) platform.TextRange {
	r = platform.NormalizeTextRange(r)
	r.Start, r.End = min(r.Start, n), min(r.End, n)
	return r
}

// target is the range an edit replaces: r, else the marked range, else
// the selection.
func (d *document) target(r *platform.TextRange) platform.TextRange {
	switch {
	case r != nil:
		return clamp(*r, units(d.text))
	case d.marked:
		return clamp(d.mark, units(d.text))
	default:
		return clamp(d.sel.Range, units(d.text))
	}
}

// splice puts s in place of range r of the text.
func (d *document) splice(r platform.TextRange, s string) {
	d.text = d.text[:at(d.text, r.Start)] + s + d.text[at(d.text, r.End):]
}

// committed is the text without the marked part.
func (d *document) committed() string {
	if !d.marked {
		return d.text
	}
	m := clamp(d.mark, units(d.text))
	return d.text[:at(d.text, m.Start)] + d.text[at(d.text, m.End):]
}

// commit queues the committed text as an Edited input; w.mu is held.
func (w *Window) commit() {
	t := w.doc.committed()
	w.doc.sent++
	w.doc.known = t
	w.queue(Input{Kind: Edited, Text: t, Edit: w.doc.sent})
}

func (c textClient) TextForRange(r platform.TextRange) (string, platform.TextRange) {
	c.w.mu.Lock()
	defer c.w.mu.Unlock()
	d := &c.w.doc
	r = clamp(r, units(d.text))
	return d.text[at(d.text, r.Start):at(d.text, r.End)], r
}

func (c textClient) Selection() platform.TextSelection {
	c.w.mu.Lock()
	defer c.w.mu.Unlock()
	return c.w.doc.sel
}

func (c textClient) MarkedRange() (platform.TextRange, bool) {
	c.w.mu.Lock()
	defer c.w.mu.Unlock()
	return c.w.doc.mark, c.w.doc.marked
}

// ReplaceText commits s in place of r (nil: the marked range, else the
// selection), ends any composition and puts the caret after s.
func (c textClient) ReplaceText(r *platform.TextRange, s string) {
	c.w.mu.Lock()
	defer c.w.mu.Unlock()
	d := &c.w.doc
	t := d.target(r)
	d.splice(t, s)
	caret := t.Start + units(s)
	d.sel = platform.TextSelection{Range: platform.TextRange{Start: caret, End: caret}}
	d.mark, d.marked = platform.TextRange{}, false
	c.w.commit()
}

// SetMarkedText composes s in place of r (nil: the marked range, else the
// selection), with the selection at sel inside s; an empty s ends the
// composition and removes it. Nothing is committed.
func (c textClient) SetMarkedText(r *platform.TextRange, s string, sel platform.TextRange) {
	c.w.mu.Lock()
	defer c.w.mu.Unlock()
	d := &c.w.doc
	t := d.target(r)
	d.splice(t, s)
	n := units(s)
	sel = clamp(sel, n)
	d.sel = platform.TextSelection{Range: platform.TextRange{Start: t.Start + sel.Start, End: t.Start + sel.End}}
	if n == 0 {
		// The composition ended with nothing: what it composed over is
		// gone from the committed text too.
		d.mark, d.marked = platform.TextRange{}, false
		if d.committed() != d.known {
			c.w.commit()
		}
		return
	}
	d.mark, d.marked = platform.TextRange{Start: t.Start, End: t.Start + n}, true
}

// UnmarkText commits the composition as it stands.
func (c textClient) UnmarkText() {
	c.w.mu.Lock()
	defer c.w.mu.Unlock()
	d := &c.w.doc
	if !d.marked {
		return
	}
	d.mark, d.marked = platform.TextRange{}, false
	c.w.commit()
}

// BoundsForRange is the caret's box, which is all the window knows of
// where the text lies.
func (c textClient) BoundsForRange(r platform.TextRange) (platform.RectF, platform.TextRange, bool) {
	c.w.mu.Lock()
	defer c.w.mu.Unlock()
	return c.w.doc.caret, clamp(r, units(c.w.doc.text)), true
}

// IndexForPoint knows no glyph positions: no index.
func (c textClient) IndexForPoint(x, y float64) (int, bool) { return 0, false }

// reconcile takes the field's value at a paint, after the program took
// the Edited inputs up to number taken; w.mu is held. While it has not
// taken them all the document stays; then a value that is not the
// committed text the program was last told (it refused or rewrote it)
// becomes the document, the caret at its end, and ends any composition.
func (w *Window) reconcile(value string, taken int) {
	d := &w.doc
	if taken < d.sent || value == d.known {
		return
	}
	n := units(value)
	*d = document{text: value, sel: platform.TextSelection{Range: platform.TextRange{Start: n, End: n}}, sent: d.sent, known: value, caret: d.caret}
}

// runeIndex is the rune offset of UTF-16 offset i of s.
func runeIndex(s string, i int) int {
	n := 0
	for _, r := range s {
		if i <= 0 {
			break
		}
		i -= utf16.RuneLen(r)
		n++
	}
	return n
}

// ComposeOver composes text over the UTF-16 range from, to of a Headless
// window's focused field (to past the end: to its end; from < 0: over
// the marked range, else the selection), as an input method does.
func (w *Window) ComposeOver(from, to int, text string) {
	var r *platform.TextRange
	if from >= 0 {
		r = &platform.TextRange{Start: from, End: to}
	}
	n := units(text)
	textClient{w}.SetMarkedText(r, text, platform.TextRange{Start: n, End: n})
}

// Commit commits a Headless window's composition, as an input method
// does when a candidate is chosen.
func (w *Window) Commit() { textClient{w}.UnmarkText() }

// EditingKeyboard is the keyboard the focused field asked for at the
// last paint ("" with none focused).
func (w *Window) EditingKeyboard() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.keyboard
}
