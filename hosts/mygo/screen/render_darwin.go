//go:build darwin

package screen

import (
	"github.com/egoist/mygo/drawing/gpu/metal"
	"github.com/egoist/mygo/drawing/platform"
)

// newGPURenderer returns a Metal renderer drawing into the surface's
// layer (macOS, iOS).
func newGPURenderer(n platform.SurfaceNative) (gpuRenderer, error) {
	if n.Layer == 0 {
		return nil, nil
	}
	return metal.New(n.Layer)
}
