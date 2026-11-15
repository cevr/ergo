package screen

import "testing"

// TestTextIsLaidOutOnce: text that does not change, measured and drawn
// frame after frame, is laid out once; the text system's cache keeps it
// (LayoutsMade counts each layout made). It kills GPa-measure-shapes and
// GPa-draw-shapes (hosts/mygo/testdata/mutants.json), which shape it
// anew at each TextWidth or each frame drawn.
func TestTextIsLaidOutOnce(t *testing.T) {
	w, err := Headless(300, 100)
	if err != nil {
		t.Fatal(err)
	}
	draw := func() {
		w.Begin()
		w.Fill(0, 0, 300, 100, 0, 0xffffffff)
		w.Text(10, 10, "laid out once "+t.Name(), 13, 0x000000ff)
		w.Text(10, 40, "and its width "+t.Name(), 13, 0x000000ff)
		w.Present()
		if !w.Render() {
			t.Fatal("the window drew no frame")
		}
	}
	measure := func() {
		w.TextWidth("and its width "+t.Name(), 13)
	}
	measure()
	draw()
	made := w.sys.LayoutsMade()
	for range 4 {
		measure()
		draw()
	}
	if n := w.sys.LayoutsMade() - made; n != 0 {
		t.Fatalf("4 frames of the same text made %d layouts", n)
	}
}
