package screen

import (
	"strconv"
	"testing"

	"github.com/egoist/mygo/drawing/scene"
)

// TestMeasuredLinesAreNotShapedAgain: a line whose words the program
// measured (TextWidth of each word and of a space, as the desktop host's
// Metrics learns its runs) is drawn from those words' layouts. Its
// changing word, a frame counter's, is shaped once a frame (to measure
// it), not again to draw the line (GP-SHAPE; MAPCOPY's RU16: the window
// shaped such text twice a frame where MyGo's ui shapes it once). It
// kills GP-SHAPE-whole-lines (hosts/mygo/testdata/mutants.json), which
// shapes every line whole.
func TestMeasuredLinesAreNotShapedAgain(t *testing.T) {
	w, err := Headless(300, 100)
	if err != nil {
		t.Fatal(err)
	}
	frame := func(i int) uint64 {
		n := strconv.Itoa(i)
		w.Begin()
		w.Fill(0, 0, 300, 100, 0, 0xffffffff)
		w.TextWidth("frame", 13)
		w.TextWidth(" ", 13)
		w.TextWidth(n, 13)
		w.Text(10, 10, "frame "+n+" of the clock", 13, 0x000000ff)
		w.Present()
		made := w.sys.LayoutsMade()
		if !w.Render() {
			t.Fatal("the window drew no frame")
		}
		return w.sys.LayoutsMade() - made
	}
	frame(0) // "of", "the" and "clock" are laid out once, at the first draw
	for i := 1; i <= 4; i++ {
		if n := frame(i); n != 0 {
			t.Fatalf("frame %d: drawing a line of measured words made %d layouts, want 0", i, n)
		}
	}
}

// TestComposedLinesDrawAsShaped: a composable line drawn from its words'
// layouts has the glyphs, places and atlas cells the line shaped whole
// has, so composing (GP-SHAPE) changes no pixel; and a line that is not
// composable (a leading space, two spaces, non-ASCII) is shaped whole. It
// kills GP-SHAPE-no-space (words set with no space between them) and
// GP-SHAPE-any-text (every line composed, a double space and a
// right-to-left line among them).
func TestComposedLinesDrawAsShaped(t *testing.T) {
	w, err := Headless(300, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{
		"frame 12 of the clock", "AV To Wa fly", "label 7", "x", "i.e. 3.14, (ok)!",
		" lead", "two  spaces", "trail ", "naïve café", "שלום עולם",
	} {
		for _, size := range []float32{9, 13, 21.5} {
			for _, scale := range []float32{1, 2} {
				var whole, composed scene.Scene
				g := glyphText{x: 10.25, y: 7, s: s, size: size, color: scene.Color{A: 1}}
				w.lineGlyphs(&whole, g, scale, false)
				w.lineGlyphs(&composed, g, scale, true)
				if len(whole.Glyphs) != len(composed.Glyphs) {
					t.Fatalf("%q at %v×%v: composed %d glyphs, whole %d", s, size, scale, len(composed.Glyphs), len(whole.Glyphs))
				}
				for i := range whole.Glyphs {
					if whole.Glyphs[i] != composed.Glyphs[i] {
						t.Fatalf("%q at %v×%v: glyph %d composed %+v, whole %+v", s, size, scale, i, composed.Glyphs[i], whole.Glyphs[i])
					}
				}
			}
		}
	}
}
