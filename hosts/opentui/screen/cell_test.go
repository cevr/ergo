//go:build (linux || darwin) && (amd64 || arm64)

package screen

import (
	"math/rand/v2"
	"testing"
)

// TestCellReadsOnlyTheLease: Screen.Cell reads the lease's planes only
// within its width × height, and what it reads is the scene's. For 30
// seeded screens (1..60 by 1..20 cells) holding a text of seeded
// printable characters in a seeded colour:
//   - every cell's text is the glyph Rows shows there, and a cell that
//     shows a character has the text's colour;
//   - a cell outside the screen (column -1 or the width, row -1 or the
//     height, on every row and column) is the zero Cell, read from no
//     plane.
//
// Replay a seed with -screen.seed=N. Mutant (testdata/mutants.json):
// OTUL-cell-past-the-edge, Cell's column check loosened to x > w, so the
// column at the width reads the next row's first cell, or past the
// planes on the last row.
func TestCellReadsOnlyTheLease(t *testing.T) {
	for _, seed := range seeds(t, 30) {
		r := rand.New(rand.NewPCG(uint64(seed), 0xce11))
		cols, lines := 1+r.IntN(60), 1+r.IntN(20)
		s := headless(t, cols, lines)
		words := make([]byte, r.IntN(cols*lines+1))
		for i := range words {
			words[i] = byte('!' + r.IntN(94))
		}
		fg := r.Uint32() | 0xff
		s.Create(1, Text)
		s.Move(1, 0, 0)
		s.SetText(1, string(words))
		s.SetLook(1, fg, 0, 0)
		if err := s.Present(); err != nil {
			t.Fatal(err)
		}
		rows := s.Rows()
		if len(rows) != lines {
			t.Fatalf("seed %d: %d rows, want %d", seed, len(rows), lines)
		}
		for y, row := range rows {
			if len(row) != cols {
				t.Fatalf("seed %d: row %d is %q, want %d cells", seed, y, row, cols)
			}
			for x := range cols {
				c := s.Cell(x, y)
				if c.Text != row[x:x+1] {
					t.Fatalf("seed %d: cell %d,%d is %q, Rows shows %q", seed, x, y, c.Text, row[x:x+1])
				}
				if c.Text != " " && c.Fg != fg {
					t.Fatalf("seed %d: cell %d,%d (%q) has colour %08x, the text's is %08x", seed, x, y, c.Text, c.Fg, fg)
				}
			}
		}
		var outside [][2]int
		for y := -1; y <= lines; y++ {
			outside = append(outside, [2]int{-1, y}, [2]int{cols, y})
		}
		for x := range cols {
			outside = append(outside, [2]int{x, -1}, [2]int{x, lines})
		}
		for _, p := range outside {
			if c := s.Cell(p[0], p[1]); c != (Cell{}) {
				t.Fatalf("seed %d: cell %d,%d outside the %dx%d screen reads %+v", seed, p[0], p[1], cols, lines, c)
			}
		}
	}
}
