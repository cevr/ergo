//go:build darwin

package screen

import (
	"testing"

	"github.com/egoist/mygo/drawing/gpu/metal"
	"github.com/egoist/mygo/drawing/platform"
	"github.com/egoist/mygo/drawing/scene"
)

// metalOffscreen is a GPU renderer that draws each frame with Metal into
// a texture of its own and keeps its pixels.
type metalOffscreen struct{ pix []byte }

func (m *metalOffscreen) Render(s *scene.Scene) (err error) {
	m.pix, err = metal.RenderOffscreen(s)
	return err
}

func (m *metalOffscreen) Release() {}

// gpuFrame runs draw with newGPU making Metal renderers that draw
// offscreen, and returns the pixels of the last frame: premultiplied BGRA
// rows from the top, as the CPU renderer's. With no Metal device it skips
// t.
func gpuFrame(t *testing.T, pw, ph int, draw func()) []byte {
	t.Helper()
	m := &metalOffscreen{}
	withGPU(t, func(platform.SurfaceNative) (gpuRenderer, error) { return m, nil })
	draw()
	if m.pix == nil {
		t.Skip("Metal drew no frame offscreen (no device)")
	}
	return m.pix
}
