//go:build (linux || darwin) && (amd64 || arm64)

package screen

import (
	"fmt"
	"testing"
)

// shows is the color a cell shows where its four pixels are one color:
// a full block's foreground or a space's background.
func shows(c Cell) string {
	switch c.Text {
	case "█":
		return fmt.Sprintf("%08x", c.Fg)
	case " ", "":
		return fmt.Sprintf("%08x", c.Bg)
	}
	return fmt.Sprintf("%q fg %08x bg %08x", c.Text, c.Fg, c.Bg)
}

// TestPixelsShowAtTheirBox: a picture given to a Box shows in the frame
// at the box's place, two pixels to a cell each way: a box moved 3 cells
// right and 2 down, of a picture red on its left half and blue on its
// right, shows red cells then blue cells there, and the cells outside it
// keep the background.
//
// Mutants: the picture drawn at the screen's corner (its place dropped),
// and a picture's pixels read as one to a cell.
func TestPixelsShowAtTheirBox(t *testing.T) {
	s := headless(t, 20, 8)
	s.Create(1, Box)
	s.Move(1, 0, 0)
	s.SetStyle(1, Inset, 0, 3)
	s.SetStyle(1, Inset, 1, 2)
	s.SetStyle(1, Absolute, -1, 1)
	s.SetStyle(1, Width, -1, 4)
	s.SetStyle(1, Height, -1, 2)
	const w, h = 8, 4
	data := make([]byte, 4*w*h)
	for y := range h {
		for x := range w {
			i := 4 * (y*w + x)
			if x < w/2 {
				copy(data[i:], []byte{255, 0, 0, 255})
			} else {
				copy(data[i:], []byte{0, 0, 255, 255})
			}
		}
	}
	s.SetPixels(1, w, data)
	if err := s.Present(); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		x, y int
		want string
	}{{3, 2, "ff0000ff"}, {4, 3, "ff0000ff"}, {5, 2, "0000ffff"}, {6, 3, "0000ffff"}, {7, 2, "00000000"}, {2, 2, "00000000"}, {3, 4, "00000000"}, {0, 0, "00000000"}} {
		if got := shows(s.Cell(c.x, c.y)); got != c.want {
			t.Errorf("cell %d,%d shows %s, want %s", c.x, c.y, got, c.want)
		}
	}
}
