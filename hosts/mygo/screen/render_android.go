package screen

import (
	"github.com/egoist/mygo/drawing/gpu/gl"
	"github.com/egoist/mygo/drawing/platform"
	"github.com/egoist/mygo/drawing/scene"
)

// newGPURenderer returns an OpenGL ES renderer drawing into the surface's
// ANativeWindow, with an EGL context of its own.
func newGPURenderer(n platform.SurfaceNative) (gpuRenderer, error) {
	if n.Window == 0 {
		return nil, nil
	}
	win, err := gl.NewWindow(n.Window)
	if err != nil {
		return nil, err
	}
	r, err := gl.New()
	if err != nil {
		win.Release()
		return nil, err
	}
	return &windowRenderer{Renderer: r, win: win}, nil
}

// windowRenderer draws a frame and shows it. An EGL window's buffers keep
// nothing from one frame to the next, and frames draw whole.
type windowRenderer struct {
	*gl.Renderer
	win *gl.Window
}

func (r *windowRenderer) Render(s *scene.Scene) error {
	r.win.MakeCurrent()
	if err := r.Renderer.Render(s); err != nil {
		return err
	}
	return r.win.Swap()
}

func (r *windowRenderer) Release() {
	r.win.MakeCurrent()
	r.Renderer.Release()
	r.win.Release()
}
