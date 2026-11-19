package screen

import (
	"runtime"
	"testing"
)

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

// TestStrokesOutlastAFullAtlas: a stroke that moves every frame, as a
// chart's line does, takes a new mask each frame, and the atlas fills;
// draw makes room and builds the frame again, so no stroke falls back to
// Lines. It kills GPs-no-room (hosts/mygo/testdata/mutants.json): without
// MakeRoom, every stroke after the atlas fills is drawn a Line a segment.
func TestStrokesOutlastAFullAtlas(t *testing.T) {
	w, err := Headless(600, 600)
	if err != nil {
		t.Fatal(err)
	}
	// Each shape shows for two frames, so the atlas keeps its mask (the
	// second frame) and fills with masks of most of the window.
	for i := range 40 {
		w.Begin()
		w.StrokeBegin(6, 0x203080ff)
		w.StrokeTo(10, 590)
		w.StrokeTo(300, float64(10+i/2))
		w.StrokeTo(590, 590)
		w.StrokeEnd()
		w.Present()
		if !w.Render() {
			t.Fatal("the window drew no frame")
		}
	}
	if w.lined != 0 {
		t.Fatalf("%d strokes of 40 frames were drawn as Lines", w.lined)
	}
}

// TestHeadlessFrameKeepsOneBuffer: a Headless window's frames allocate
// far less than a frame buffer each (the bytes the runtime counts as
// allocated), since the frame stays in the renderer's image. It kills
// GPm-present-headless (hosts/mygo/testdata/mutants.json), which copies
// each frame into the fake surface, a new buffer a frame: in dash-gui,
// 3.8 GB over 800 frames and a peak RSS of 65 MiB, not 38.
func TestHeadlessFrameKeepsOneBuffer(t *testing.T) {
	w, err := Headless(900, 700)
	if err != nil {
		t.Fatal(err)
	}
	frame(t, w, 0, false)
	var a, b runtime.MemStats
	runtime.ReadMemStats(&a)
	for i := range 8 {
		frame(t, w, float64(i), false)
	}
	runtime.ReadMemStats(&b)
	if per := (b.TotalAlloc - a.TotalAlloc) / 8; per > 900*700*4/4 {
		t.Fatalf("a frame allocated %d bytes, a quarter of a frame buffer is %d", per, 900*700)
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
