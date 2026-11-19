package screen

import "testing"

// TestTextSlants: a run TextSlanted draws leans right (its ink's middle
// in the top rows of the glyph is right of the middle in the bottom
// rows by about slant of the glyph's height), and a run Text draws
// stands upright, read off the pixels of a headless window. Mutant
// (hosts/mygo ledger): ink-italic-upright (TextSlanted's run is drawn
// with no shear).
func TestTextSlants(t *testing.T) {
	lean := func(slanted bool) (float64, int) {
		w, err := Headless(120, 120)
		if err != nil {
			t.Fatal(err)
		}
		w.Begin()
		w.Fill(0, 0, 120, 120, 0, 0xffffffff)
		if slanted {
			w.TextSlanted(40, 20, "l", 64, 0x000000ff)
		} else {
			w.Text(40, 20, "l", 64, 0x000000ff)
		}
		w.Present()
		w.Render()
		// Each row's mean x of dark pixels, over the rows with ink.
		var rows []float64
		for y := 0; y < 120; y++ {
			sum, n := 0, 0
			for x := 0; x < 120; x++ {
				if w.Pixel(x, y)>>24 < 96 {
					sum += x
					n++
				}
			}
			if n > 0 {
				rows = append(rows, float64(sum)/float64(n))
			}
		}
		if len(rows) < 12 {
			t.Fatalf("slanted %v: the run has %d rows of ink, want a glyph", slanted, len(rows))
		}
		q := len(rows) / 4
		top, bottom := 0.0, 0.0
		for i := 0; i < q; i++ {
			top += rows[i]
			bottom += rows[len(rows)-1-i]
		}
		return (top - bottom) / float64(q), len(rows)
	}
	upright, _ := lean(false)
	oblique, height := lean(true)
	if upright < -1 || upright > 1 {
		t.Errorf("an upright run leans %.2f pixels from bottom to top, want 0", upright)
	}
	// The top quarter's middle is 3/4 of the height above the bottom
	// quarter's: slant of that, less a pixel for rounding.
	if want := slant*0.75*float64(height) - 1; oblique < want {
		t.Errorf("a slanted run leans %.2f pixels from bottom to top over %d rows, want at least %.2f", oblique, height, want)
	}
}
