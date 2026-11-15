package screen

import (
	"fmt"
	"runtime"
	"testing"
)

// busy draws one list of a busy frame on w: 40 rectangles, 40 lines of
// text, 40 accessibility nodes and a stroke of 40 points that moves by i
// quarter pixels, so that its mask is rasterized anew; then renders it.
func busy(t *testing.T, w *Window, i int) {
	t.Helper()
	w.Begin()
	w.Fill(0, 0, 400, 400, 0, 0xffffffff)
	for k := range 40 {
		y := float64(k * 10)
		w.Fill(0, y, 50, 8, 2, 0x808080ff)
		w.Text(60, y, labels[k], 9, 0x000000ff)
		w.Access(k+1, -1, RoleButton, labels[k], "", 0, y, 50, 8, false, false)
	}
	w.StrokeBegin(2, 0x203080ff)
	for k := range 40 {
		w.StrokeTo(200+float64(k*4)+float64(i%4)/4, 50+float64(k%7*9))
	}
	w.StrokeEnd()
	w.Present()
	if !w.Render() {
		t.Fatal("the window drew no frame")
	}
}

var labels = func() []string {
	out := make([]string, 40)
	for k := range out {
		out[k] = fmt.Sprintf("label %d", k)
	}
	return out
}()

// TestBusyFrameAllocatesLittle: once its buffers have grown, a busy
// frame allocates a few objects and few bytes, whatever it draws: its
// lists reuse the buffers of the list shown before, a stroke's points
// live in its list's arena, the accessibility tree that did not change
// is not published again, and a mask is rasterized into one buffer. It
// counts the runtime's allocations (MemStats), not time. It kills
// GPa-lists-new, GPa-points-new, GPa-access-always and GPa-pix-new
// (hosts/mygo/testdata/mutants.json).
func TestBusyFrameAllocatesLittle(t *testing.T) {
	w, err := Headless(400, 400)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 8 {
		busy(t, w, i)
	}
	var a, b runtime.MemStats
	runtime.ReadMemStats(&a)
	for i := range 16 {
		busy(t, w, i)
	}
	runtime.ReadMemStats(&b)
	bytes, objects := (b.TotalAlloc-a.TotalAlloc)/16, (b.Mallocs-a.Mallocs)/16
	// 464 bytes and 5 objects when written; one list of the busy frame
	// before is over 30 kB.
	if bytes > 1024 || objects > 8 {
		t.Fatalf("a busy frame allocated %d bytes in %d objects, want at most 1024 in 8", bytes, objects)
	}
}

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
