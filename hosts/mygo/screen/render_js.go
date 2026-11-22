//go:build js

package screen

import (
	"github.com/egoist/mygo/drawing/gpu/gl"
	"github.com/egoist/mygo/drawing/platform"
)

// newGPURenderer returns a WebGL 2 renderer for a canvas whose context is
// current while it draws a frame; a canvas that draws in memory (no WebGL
// 2) has none. Frames draw whole.
func newGPURenderer(n platform.SurfaceNative) (gpuRenderer, error) {
	if n.Canvas == 0 {
		return nil, nil
	}
	return gl.New()
}
