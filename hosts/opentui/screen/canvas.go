package screen

import (
	"fmt"
	"math"

	"ergo.local/prototype/hosts/opentui/ot"
)

// A canvas is a Box that shows a picture: RGBA pixels, two to a cell
// each way, drawn over the frame at the box's place by OpenTUI's
// supersampling (each cell's four pixels become a quadrant block of two
// colors).

// picture is a canvas's pixels: rows of width RGBA pixels.
type picture struct {
	width int
	data  []byte
}

// SetPixels gives the Box n the picture data: rows of width RGBA pixels
// (four bytes each), two pixels to a cell across and down, drawn from the
// box's top left corner in each frame. Empty data takes the picture away.
func (s *Screen) SetPixels(n, width int, data []byte) {
	s.record(command{op: opPixels, node: n, kind: width, data: append([]byte(nil), data...)})
}

// pixels keeps or drops a node's picture (owner thread).
func (s *Screen) pixels(c command) error {
	if c.kind <= 0 || len(c.data) == 0 {
		delete(s.pictures, c.node)
		return nil
	}
	if len(c.data)%(4*c.kind) != 0 {
		return fmt.Errorf("screen: SetPixels %d: %d bytes are no whole rows of %d pixels", c.node, len(c.data), c.kind)
	}
	s.pictures[c.node] = picture{width: c.kind, data: c.data}
	return nil
}

// draw draws each picture into the frame req, at its node's place in it
// (owner thread).
func (s *Screen) draw(req *ot.SceneFrameRequest) error {
	for n, p := range s.pictures {
		h, ok := s.nodes[n]
		if !ok {
			continue
		}
		if err := check(CallLayout, n); err != nil {
			return err
		}
		s.calls++
		l, err := ot.SceneGetLayout(s.ctx, h, ot.OT_LAYOUT_PAINT)
		if err != nil {
			return fail(CallLayout, n, fmt.Errorf("screen: layout of %d: %w", n, err))
		}
		if err := check(CallPaint, n); err != nil {
			return err
		}
		s.calls++
		if err := ot.BufferDrawSupersample(s.ctx, s.session, req, p.data, int32(math.Round(l.ScreenX)), int32(math.Round(l.ScreenY)), 1, uint32(4*p.width)); err != nil {
			return fail(CallPaint, n, fmt.Errorf("screen: draw %d: %w", n, err))
		}
	}
	return nil
}
