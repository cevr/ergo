package screen

import "testing"

// polyline is a zigzag with a sharp turn, its points x then y in DIPs.
var polyline = []float64{20, 80, 60, 20, 100, 70, 140, 25, 180, 75}

// frame draws one list on w: the polyline as one stroke (StrokeBegin)
// moved by dx, or as a Line a segment, then renders it.
func frame(t *testing.T, w *Window, dx float64, lines bool) {
	t.Helper()
	w.Begin()
	w.Fill(0, 0, 200, 100, 0, 0xffffffff)
	if lines {
		for i := 2; i < len(polyline); i += 2 {
			w.Line(polyline[i-2]+dx, polyline[i-1], polyline[i]+dx, polyline[i+1], 6, 0x203080ff)
		}
	} else {
		w.StrokeBegin(6, 0x203080ff)
		for i := 0; i < len(polyline); i += 2 {
			w.StrokeTo(polyline[i]+dx, polyline[i+1])
		}
		w.StrokeEnd()
	}
	w.Present()
	if !w.Render() {
		t.Fatal("the window drew no frame")
	}
}

// TestStrokeMaskIsKept: a stroke whose points do not change is
// rasterized at most twice (the atlas keeps a mask from the second frame
// that draws it) and never again while they stay; moved by whole pixels
// it keeps its mask, and a moved point draws a new one. It kills
// GPs-mask-every-frame (hosts/mygo/testdata/mutants.json), which keys a
// mask by the frame: the frames would keep rasterizing it.
func TestStrokeMaskIsKept(t *testing.T) {
	w, err := Headless(200, 100)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		frame(t, w, 0, false)
	}
	kept := w.MasksDrawn()
	if kept < 1 || kept > 2 {
		t.Fatalf("two frames of one stroke drew %d masks, want 1 or 2", kept)
	}
	for range 4 {
		frame(t, w, 0, false)
	}
	frame(t, w, 3, false)
	if n := w.MasksDrawn(); n != kept {
		t.Fatalf("the same stroke, and the same moved by 3 pixels, drew %d masks after %d", n, kept)
	}
	frame(t, w, 0.5, false)
	if w.MasksDrawn() == kept {
		t.Fatal("a stroke moved by half a pixel drew no new mask")
	}
}

// TestStrokePaintsWhatLinesPaint: a stroke covers the pixels its Lines
// cover, a segment's rounded rectangle each: at every pixel the two
// frames are within one antialiasing step, and few pixels differ at all.
// It kills GPs-no-discs (hosts/mygo/testdata/mutants.json): without a
// disc at each point, the caps and the outer side of each turn are
// missing.
func TestStrokePaintsWhatLinesPaint(t *testing.T) {
	a, err := Headless(200, 100)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Headless(200, 100)
	if err != nil {
		t.Fatal(err)
	}
	frame(t, a, 0, false)
	frame(t, b, 0, true)
	inked, off := 0, 0
	for y := range 100 {
		for x := range 200 {
			p, q := a.Pixel(x, y), b.Pixel(x, y)
			if q != 0xffffffff {
				inked++
			}
			worst := 0
			for s := 8; s < 32; s += 8 {
				d := int(p>>s&0xff) - int(q>>s&0xff)
				worst = max(worst, d, -d)
			}
			if worst > 160 {
				t.Fatalf("pixel %d,%d: the stroke paints %08x, its Lines %08x", x, y, p, q)
			}
			if worst > 48 {
				off++
			}
		}
	}
	if inked == 0 || off*20 > inked {
		t.Fatalf("%d of the %d pixels the Lines ink differ by more than 48", off, inked)
	}
}
