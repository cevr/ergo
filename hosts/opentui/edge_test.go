package opentui

import (
	"fmt"
	"strings"
	"testing"
)

// edgeScroll is ui.EdgeScroll (std ui, zone.ergo), the reference
// TestPlainScrollEdges holds the terminal to: the offset a held pointer
// at y in a viewport of height over content moves a Scroll to in a frame,
// with ui.EdgeBand 32 and ui.EdgeStep 16 DIPs.
func edgeScroll(offset, height, content, y float64) float64 {
	const band, step = 32.0, 16.0
	most := max(content-height, 0)
	next := min(max(offset, 0), most)
	switch {
	case y < band:
		next -= step * (band - y) / band
	case y > height-band:
		next += step * (y - (height - band)) / band
	}
	return min(max(next, 0), most)
}

// TestPlainScrollEdges: on the pane app (cmd/hostagree: an Area of 10
// rows over a plain Scroll of 40 lines), while the pointer pressed in the
// Area stays near its bottom or top edge, each frame scrolls the Scroll
// by ui.EdgeScroll, and the terminal shows the offset whole cells nearest
// the exact one; after the Up it stays. ui.TestHost has no layout and
// models the edge scroll of lists only (law EdgeScrollRunsEachFrame), so
// the reference here is EdgeScroll itself, run a frame at a time; the
// trees still agree with TestHost at every step.
//
// It builds and runs a Go binary because the driver is Ergo source that
// calls Go (go.Foreign): no in-process check runs it, and hostagree is the
// program that does.
//
// Mutant OT5-edge-scroll-plain-scroll-still: an Area scrolls at its edge
// only a list, never a plain Scroll.
func TestPlainScrollEdges(t *testing.T) {
	bin, err := build("hostagree", "./cmd/hostagree")
	if err != nil {
		t.Fatal(err)
	}
	const height, content, cell = 160.0, 640.0, 16.0
	type hold struct {
		step   string
		y      float64
		frames int
	}
	plan := []hold{
		{"point pane down 40 144", 144, 5},
		{"", 144, 100},
		{"point pane move 40 0", 0, 3},
		{"point pane move 40 80", 80, 7},
		{"point pane move 40 16", 16, 4},
		{"point pane up 40 16", -1, 9},
	}
	var script strings.Builder
	script.WriteString("pane\n")
	exact := 0.0
	var want []string
	for _, h := range plan {
		if h.step != "" {
			script.WriteString(h.step + "\n")
		}
		fmt.Fprintf(&script, "frames %d\nscrolls\n", h.frames)
		for i := 0; i < h.frames && h.y >= 0; i++ {
			exact = edgeScroll(exact, height, content, h.y)
		}
		want = append(want, fmt.Sprintf("scrolls %d", int(exact/cell+0.5)))
	}
	text, err := play(bin, script.String())
	if err != nil {
		t.Fatalf("%v\n%s", err, text)
	}
	var got []string
	for _, line := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		switch {
		case strings.HasPrefix(line, "scrolls"):
			got = append(got, line)
		case strings.HasPrefix(line, "ok "), line == "end":
		default:
			t.Errorf("%s", line)
		}
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("the Scroll's offsets (cells):\n%s\nwant:\n%s\nscript:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"), script.String())
	}
}
