package screen

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

// TestImageDrawsItsPart is G9 UI-IMAGE's window half: Image draws the
// region of an atlas it names (not the whole image), under the affine it
// is given, stretched into its rectangle; an image file that is not there
// draws nothing. The atlas is 8 by 4: red on its left half, blue on its
// right.
func TestImageDrawsItsPart(t *testing.T) {
	atlas := image.NewNRGBA(image.Rect(0, 0, 8, 4))
	for y := range 4 {
		for x := range 8 {
			c := color.NRGBA{R: 255, A: 255}
			if x >= 4 {
				c = color.NRGBA{B: 255, A: 255}
			}
			atlas.SetNRGBA(x, y, c)
		}
	}
	src := filepath.Join(t.TempDir(), "atlas.png")
	f, err := os.Create(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, atlas); err != nil {
		t.Fatal(err)
	}
	f.Close()
	w, err := Headless(60, 60)
	if err != nil {
		t.Fatal(err)
	}
	const white, red, blue = 0xffffffff, 0xff0000ff, 0x0000ffff
	w.Begin()
	w.Fill(0, 0, 60, 60, 0, white)
	// The atlas's right half (blue) into 0, 0, 20, 20, moved by 20, 20.
	w.Image(src, 4, 0, 4, 4, 1, 0, 0, 1, 20, 20, 0, 0, 20, 20)
	// The whole atlas into 0, 0, 16, 8: red on its left, blue on its right.
	w.Image(src, 0, 0, 0, 0, 1, 0, 0, 1, 0, 0, 0, 0, 16, 8)
	// No file there: nothing drawn.
	w.Image(filepath.Join(t.TempDir(), "none.png"), 0, 0, 0, 0, 1, 0, 0, 1, 0, 0, 0, 40, 20, 20)
	w.Present()
	w.Render()
	for _, c := range []struct {
		x, y int
		want uint32
		what string
	}{
		{22, 30, blue, "the region's left edge, moved"},
		{30, 30, blue, "the region's middle, moved"},
		{10, 10, white, "where the region would be without the move"},
		{2, 4, red, "the whole image's left"},
		{13, 4, blue, "the whole image's right"},
		{10, 50, white, "a missing image"},
	} {
		if got := w.Pixel(c.x, c.y); got != c.want {
			t.Errorf("pixel %d,%d (%s) is %08x, want %08x", c.x, c.y, c.what, got, c.want)
		}
	}
}
