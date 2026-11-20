//go:build linux && !android && (amd64 || arm64)

package screen

import (
	"github.com/egoist/mygo/drawing/gpu/gl"
	"github.com/egoist/mygo/drawing/platform"
	"github.com/egoist/mygo/drawing/scene"
)

// newGPURenderer returns an OpenGL renderer for a surface that draws in a
// GtkGLArea's render signal; a surface that draws in memory has none.
func newGPURenderer(n platform.SurfaceNative) (gpuRenderer, error) {
	if n.GLArea == 0 {
		return nil, nil
	}
	// The renderer reads the version of the context current.
	gl.MakeAreaCurrent(n.GLArea)
	r, err := gl.New()
	if err != nil {
		return nil, err
	}
	return &areaRenderer{Renderer: r, area: n.GLArea}, nil
}

// areaRenderer draws into a GtkGLArea with the area's context current:
// another window's frame may have left another current since GTK made it
// current. Frames draw whole.
type areaRenderer struct {
	*gl.Renderer
	area uintptr
}

func (r *areaRenderer) Render(s *scene.Scene) error {
	gl.MakeAreaCurrent(r.area)
	return r.Renderer.Render(s)
}
