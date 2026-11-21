package screen

import "testing"

// wide is a long zigzag with an L-turn, wider than a tile: points x then
// y in DIPs, on a window of 4400 by 300.
var wide = []float64{30, 270, 2200, 40, 4370, 270, 4370, 150}

// paint draws pts on w as one stroke, or as a Line a segment, over a
// white window of width by height, then renders it.
func paint(t *testing.T, w *Window, pts []float64, width, height float64, lines bool) {
	t.Helper()
	w.Begin()
	w.Fill(0, 0, width, height, 0, 0xffffffff)
	if lines {
		for i := 2; i < len(pts); i += 2 {
			w.Line(pts[i-2], pts[i-1], pts[i], pts[i+1], 6, 0x203080ff)
		}
	} else {
		w.StrokeBegin(6, 0x203080ff)
		for i := 0; i < len(pts); i += 2 {
			w.StrokeTo(pts[i], pts[i+1])
		}
		w.StrokeEnd()
	}
	w.Present()
	if !w.Render() {
		t.Fatal("the window drew no frame")
	}
}

// differ is how many pixels of a and b, width by height, differ by more
// than off in a channel, and the largest difference.
func differ(a, b *Window, width, height, off int) (n, worst int) {
	for y := range height {
		for x := range width {
			p, q := a.Pixel(x, y), b.Pixel(x, y)
			d := 0
			for s := 8; s < 32; s += 8 {
				e := int(p>>s&0xff) - int(q>>s&0xff)
				d = max(d, e, -e)
			}
			worst = max(worst, d)
			if d > off {
				n++
			}
		}
	}
	return n, worst
}

// TestWideStrokeIsTiled: a stroke wider than the largest atlas (4096)
// draws as tiles, not a Line a segment, from its second frame at least,
// and paints what its Lines paint.
// It kills GPl-no-tiles (hosts/mygo/testdata/mutants.json), which draws
// it as one mask: the atlas leaves it out and it falls back to Lines.
func TestWideStrokeIsTiled(t *testing.T) {
	a, err := Headless(4400, 300)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Headless(4400, 300)
	if err != nil {
		t.Fatal(err)
	}
	// The first frame may find the atlas too small for its tiles, as
	// MyGo's frames may: it grows it, and the frames after hold them.
	paint(t, a, wide, 4400, 300, false)
	first := a.lined
	for range 3 {
		paint(t, a, wide, 4400, 300, false)
	}
	paint(t, b, wide, 4400, 300, true)
	if a.lined != first {
		t.Fatalf("%d of the 3 frames after the first fell back to Lines", a.lined-first)
	}
	if n, worst := differ(a, b, 4400, 300, 48); worst > 160 || n > 400 {
		t.Fatalf("%d pixels differ from the Lines' by more than 48, at most %d", n, worst)
	}
}

// TestStrokeTilesMeetAsOneMask: a stroke cut into tiles of 17 pixels
// paints, within one step of rounding, what its one mask paints, and
// rasterizes only the tiles it reaches, not all of them (32 of 50 for
// this zigzag). It kills GPl-tile-not-moved (each tile drawn at the mask's
// corner) and GPl-tile-all (every tile rasterized), both in
// hosts/mygo/testdata/mutants.json.
func TestStrokeTilesMeetAsOneMask(t *testing.T) {
	a, err := Headless(200, 100)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Headless(200, 100)
	if err != nil {
		t.Fatal(err)
	}
	paint(t, b, polyline, 200, 100, false)
	defer func(tile int) { maskTile = tile }(maskTile)
	maskTile = 17
	paint(t, a, polyline, 200, 100, false)
	if n, worst := differ(a, b, 200, 100, 2); n > 0 {
		t.Fatalf("%d pixels of the tiles differ from the one mask's, at most %d", n, worst)
	}
	// The mask is 169 by 69 pixels: 10 by 5 tiles of 17.
	if m := a.MasksDrawn(); m >= 10*5 {
		t.Fatalf("the tiles rasterized %d masks of 50", m)
	}
}
