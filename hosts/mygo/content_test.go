package mygo

import (
	"testing"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/drawing/fake"
	"github.com/egoist/mygo/drawing/platform"
	"github.com/egoist/mygo/drawing/raster"
	"github.com/egoist/mygo/drawing/scene"
	"github.com/egoist/mygo/drawing/surface"
	"github.com/egoist/mygo/drawing/text"
)

// fill is a content outside MyGo: it paints one filled rectangle through
// the display list and the CPU renderer, and keeps the pointer presses
// the surface sends.
type fill struct {
	conn    *surface.Conn
	r       raster.Renderer
	rect    scene.Rect
	color   scene.Color
	presses [][2]float64
}

var _ mygo.Content = (*fill)(nil)

func (c *fill) AttachContent(conn *surface.Conn) {
	c.conn = conn
	conn.Event = c.event
	conn.Surface.RequestFrame()
}

func (c *fill) event(ev platform.SurfaceEvent) bool {
	switch ev.Kind {
	case platform.SurfaceFrame:
		w, h, scale := c.conn.Surface.Size()
		s := &scene.Scene{
			Width: int(w * scale), Height: int(h * scale), Scale: float32(scale), Zoom: 1,
			Clear: scene.Color{R: 255, G: 255, B: 255, A: 255},
			Ops:   []scene.Op{{Kind: scene.OpFill, Rect: c.rect, Transform: scene.Translation(0, 0), Color: c.color}},
		}
		c.r.Render(s)
		c.conn.Surface.PresentPixels(c.r.Image.Pix, c.r.Image.Stride, c.r.Image.W, c.r.Image.H)
	case platform.PointerDown:
		c.presses = append(c.presses, [2]float64{ev.X, ev.Y})
	}
	return false
}

// events is a window handler that hears only the surface's events.
type events struct {
	platform.WindowHandler
	conn *surface.Conn
}

func (h *events) SurfaceEvent(ev platform.SurfaceEvent) bool { return h.conn.Event(ev) }

// TestContentOutsideMyGo is H3f's acceptance: a package outside MyGo
// implements mygo.Content through the exported drawing layer, attaches to
// a surface of MyGo's fake backend (no window, no display), paints a
// frame with the display list and the CPU renderer, hears a press at its
// place, and measures text with the system's text engine.
//
// Mutant it kills: drawgen's package list without surface, platform,
// scene, raster, text or fake (the export no longer reaches a layer a
// host needs; TestMirrorsInternal checks only the packages listed).
func TestContentOutsideMyGo(t *testing.T) {
	b := fake.New()
	h := &events{conn: &surface.Conn{}}
	nw, err := b.NewWindow(&platform.WindowOptions{Surface: true, Width: 120, Height: 80}, h)
	if err != nil {
		t.Fatal(err)
	}
	fw := nw.(*fake.Window)
	h.conn.Surface = fw.Surface()
	h.conn.Invalidate = h.conn.Surface.RequestFrame
	h.conn.Post = func(fn func()) { fn() }
	c := &fill{rect: scene.Rect{X: 20, Y: 10, W: 40, H: 30}, color: scene.Color{R: 200, G: 30, B: 60, A: 255}}
	c.AttachContent(h.conn)

	fs := fw.FakeSurface()
	if !fs.Frame() {
		t.Fatal("the content asked for no frame")
	}
	// Pixel returns BGRA.
	if got, want := fs.Pixel(40, 25), [4]byte{60, 30, 200, 255}; got != want {
		t.Errorf("pixel inside the fill = %v, want %v", got, want)
	}
	if got, want := fs.Pixel(5, 5), [4]byte{255, 255, 255, 255}; got != want {
		t.Errorf("pixel outside the fill = %v, want %v", got, want)
	}
	fs.Send(platform.SurfaceEvent{Kind: platform.PointerDown, X: 40, Y: 25})
	if len(c.presses) != 1 || c.presses[0] != [2]float64{40, 25} {
		t.Errorf("presses = %v, want one at (40, 25)", c.presses)
	}
	l := text.Shared().Layout(text.Params{Text: "Ergo", Style: text.Style{Size: 16}})
	if l.Width <= 0 || l.Height <= 0 {
		t.Errorf("text measures %v × %v", l.Width, l.Height)
	}
}
