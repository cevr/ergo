//go:build !darwin && !android && !js && !(linux && (amd64 || arm64))

package screen

import "github.com/egoist/mygo/drawing/platform"

// newGPURenderer has no GPU renderer for this platform (Windows among
// them, until a unit takes Direct3D): frames draw in memory.
func newGPURenderer(platform.SurfaceNative) (gpuRenderer, error) {
	return nil, nil
}
